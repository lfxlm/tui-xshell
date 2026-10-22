package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"tui-ssh-workbench/internal/model"
)

// CatalogImportResult counts rows inserted by an import. Existing identical
// records are left in place, making an export safe to import more than once.
type CatalogImportResult struct {
	Groups           int
	Hosts            int
	PortForwardRules int
}

// ImportCatalog atomically imports groups, hosts and saved forwarding rules.
// ID collisions with different data and unique-name conflicts abort and roll
// back the entire import. Existing credential references are never replaced.
func (s *Store) ImportCatalog(ctx context.Context, groups []model.Group, hosts []model.Host, rules []model.PortForwardRule) (CatalogImportResult, error) {
	var result CatalogImportResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("import session catalog: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existingGroups, err := readGroupIndex(ctx, tx)
	if err != nil {
		return result, err
	}
	orderedGroups, err := orderImportedGroups(groups, existingGroups)
	if err != nil {
		return result, err
	}
	for _, group := range orderedGroups {
		if existing, ok := existingGroups[group.Id]; ok {
			if !samePortableGroup(existing, group) {
				return result, fmt.Errorf("分组 ID %q 已存在，但配置不同", group.Id)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO groups (id, name, parent_id, remote_start_dir) VALUES (?, ?, ?, ?)`,
			group.Id, group.Name, nullableID(group.ParentId), group.RemoteStartDir,
		); err != nil {
			return result, fmt.Errorf("导入分组 %q 失败：%w", group.Name, err)
		}
		existingGroups[group.Id] = group
		result.Groups++
	}

	existingHosts, err := readHostSettingsIndex(ctx, tx)
	if err != nil {
		return result, err
	}
	incomingHosts := make(map[string]model.Host, len(hosts))
	for index, host := range hosts {
		if strings.TrimSpace(host.Id) == "" {
			return result, fmt.Errorf("导入主机第 %d 项缺少 ID", index+1)
		}
		host.GroupId = strings.TrimSpace(host.GroupId)
		host.Password = ""
		host.CredentialRef = ""
		host.AuthProfileID = ""
		host.ConnectionStatus = ""
		if err := normalizeHost(&host); err != nil {
			return result, fmt.Errorf("导入主机 %q 无效：%w", host.Name, err)
		}
		if host.ProxyJumpList == nil {
			host.ProxyJumpList = []string{}
		}
		if _, duplicate := incomingHosts[host.Id]; duplicate {
			return result, fmt.Errorf("导入文件包含重复主机 ID %q", host.Id)
		}
		incomingHosts[host.Id] = host
	}
	for _, host := range incomingHosts {
		if host.GroupId != "" {
			if _, exists := existingGroups[host.GroupId]; !exists {
				return result, fmt.Errorf("主机 %q 引用了不存在的分组 %q", host.Name, host.GroupId)
			}
		}
		seenJumps := make(map[string]struct{}, len(host.ProxyJumpList))
		for _, jumpID := range host.ProxyJumpList {
			if jumpID == host.Id {
				return result, fmt.Errorf("主机 %q 不能把自己设为跳板机", host.Name)
			}
			if _, duplicate := seenJumps[jumpID]; duplicate {
				return result, fmt.Errorf("主机 %q 重复引用跳板机 %q", host.Name, jumpID)
			}
			seenJumps[jumpID] = struct{}{}
			jumpHost, exists := existingHosts[jumpID]
			if imported, ok := incomingHosts[jumpID]; ok {
				jumpHost, exists = imported, true
			}
			if !exists || jumpHost.Kind != model.HostKindSSH {
				return result, fmt.Errorf("主机 %q 引用了不存在或非 SSH 的跳板机 %q", host.Name, jumpID)
			}
		}
	}
	combinedHosts := make(map[string]model.Host, len(existingHosts)+len(incomingHosts))
	for id, host := range existingHosts {
		combinedHosts[id] = host
	}
	for id, host := range incomingHosts {
		combinedHosts[id] = host
	}
	if err := validateImportedProxyJumpChains(incomingHosts, combinedHosts); err != nil {
		return result, err
	}

	for _, host := range hosts {
		normalized := incomingHosts[host.Id]
		var existing model.Host
		err := scanHost(tx.QueryRowContext(ctx, hostSelect+` WHERE id = ?`, normalized.Id), &existing)
		switch {
		case err == nil:
			if !samePortableHost(existing, normalized) {
				return result, fmt.Errorf("主机 ID %q 已存在，但配置不同", normalized.Id)
			}
			continue
		case errors.Is(err, sql.ErrNoRows):
		default:
			return result, fmt.Errorf("检查主机 ID %q 失败：%w", normalized.Id, err)
		}
		proxyJumps, err := marshalProxyJumps(normalized.ProxyJumpList)
		if err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hosts (
				id, kind, name, address, group_id, description, port, username, startup,
			credential_ref, proxy_jump_list, tls_mode, ftp_active_mode, favorite, file_protocol, identity_file, terminal_protocol, x11_forwarding,
				serial_baud_rate, serial_data_bits, serial_parity, serial_stop_bits, serial_flow_control, terminal_encoding, terminal_input_encoding
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, normalized.Id, normalized.Kind, normalized.Name, normalized.IP,
			nullableID(normalized.GroupId), normalized.Description, normalized.Port,
			normalized.User, normalized.Startup, proxyJumps, normalized.TLSMode, normalized.FTPActiveMode, normalized.Favorite, normalized.FileProtocol, normalized.TerminalProtocol, normalized.X11Forwarding,
			normalized.SerialBaudRate, normalized.SerialDataBits, normalized.SerialParity, normalized.SerialStopBits, normalized.SerialFlowControl, normalized.TerminalEncoding, normalized.TerminalInputEncoding); err != nil {
			return result, fmt.Errorf("导入主机 %q 失败：%w", normalized.Name, err)
		}
		existingHosts[normalized.Id] = normalized
		combinedHosts[normalized.Id] = normalized
		result.Hosts++
	}

	seenRuleIDs := make(map[string]struct{}, len(rules))
	for index, rule := range rules {
		if strings.TrimSpace(rule.Id) == "" {
			return result, fmt.Errorf("导入端口转发规则第 %d 项缺少 ID", index+1)
		}
		if _, duplicate := seenRuleIDs[rule.Id]; duplicate {
			return result, fmt.Errorf("导入文件包含重复端口转发规则 ID %q", rule.Id)
		}
		seenRuleIDs[rule.Id] = struct{}{}
		rule.HostId = strings.TrimSpace(rule.HostId)
		if err := normalizePortForwardRule(&rule); err != nil {
			return result, fmt.Errorf("端口转发规则 %q 无效：%w", rule.Id, err)
		}
		if host, exists := combinedHosts[rule.HostId]; !exists || host.Kind != model.HostKindSSH {
			return result, fmt.Errorf("端口转发规则 %q 引用了不存在或非 SSH 的主机 %q", rule.Id, rule.HostId)
		}
		var existing model.PortForwardRule
		err := tx.QueryRowContext(ctx, `
			SELECT id, host_id, kind, listen_address, remote_address
			FROM port_forward_rules WHERE id = ?
		`, rule.Id).Scan(&existing.Id, &existing.HostId, &existing.Kind, &existing.ListenAddress, &existing.RemoteAddress)
		switch {
		case err == nil:
			if existing != rule {
				return result, fmt.Errorf("端口转发规则 ID %q 已存在，但配置不同", rule.Id)
			}
			continue
		case errors.Is(err, sql.ErrNoRows):
		default:
			return result, fmt.Errorf("检查端口转发规则 ID %q 失败：%w", rule.Id, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO port_forward_rules (id, host_id, kind, listen_address, remote_address)
			VALUES (?, ?, ?, ?, ?)
		`, rule.Id, rule.HostId, rule.Kind, rule.ListenAddress, rule.RemoteAddress); err != nil {
			return result, fmt.Errorf("导入端口转发规则 %q 失败：%w", rule.Id, err)
		}
		result.PortForwardRules++
	}

	if err := tx.Commit(); err != nil {
		return CatalogImportResult{}, fmt.Errorf("import session catalog: commit: %w", err)
	}
	return result, nil
}

func samePortableGroup(existing, imported model.Group) bool {
	existing.AuthProfileID = ""
	imported.AuthProfileID = ""
	existing.LocalStartDir = ""
	imported.LocalStartDir = ""
	return existing == imported
}

func readGroupIndex(ctx context.Context, tx *sql.Tx) (map[string]model.Group, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, parent_id, remote_start_dir FROM groups`)
	if err != nil {
		return nil, fmt.Errorf("导入目录时读取现有分组失败：%w", err)
	}
	defer rows.Close()
	groups := make(map[string]model.Group)
	for rows.Next() {
		var group model.Group
		var parentID sql.NullString
		if err := rows.Scan(&group.Id, &group.Name, &parentID, &group.RemoteStartDir); err != nil {
			return nil, fmt.Errorf("导入目录时读取现有分组失败：%w", err)
		}
		group.ParentId = parentID.String
		groups[group.Id] = group
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("导入目录时遍历现有分组失败：%w", err)
	}
	return groups, nil
}

func orderImportedGroups(groups []model.Group, existing map[string]model.Group) ([]model.Group, error) {
	incoming := make(map[string]model.Group, len(groups))
	for index, group := range groups {
		group.Id = strings.TrimSpace(group.Id)
		group.AuthProfileID = ""
		group.LocalStartDir = ""
		if group.Id == "" {
			return nil, fmt.Errorf("导入分组第 %d 项缺少 ID 或名称", index+1)
		}
		if err := normalizeGroup(&group); err != nil {
			return nil, fmt.Errorf("导入分组第 %d 项无效：%w", index+1, err)
		}
		if _, duplicate := incoming[group.Id]; duplicate {
			return nil, fmt.Errorf("导入文件包含重复分组 ID %q", group.Id)
		}
		incoming[group.Id] = group
	}
	state := make(map[string]uint8, len(incoming))
	ordered := make([]model.Group, 0, len(incoming))
	var visit func(string) error
	visit = func(id string) error {
		group := incoming[id]
		switch state[id] {
		case 1:
			return fmt.Errorf("导入分组中存在循环父级关系（涉及 %q）", id)
		case 2:
			return nil
		}
		state[id] = 1
		if group.ParentId != "" {
			if _, ok := incoming[group.ParentId]; ok {
				if err := visit(group.ParentId); err != nil {
					return err
				}
			} else if _, ok := existing[group.ParentId]; !ok {
				return fmt.Errorf("分组 %q 引用了不存在的父分组 %q", group.Name, group.ParentId)
			}
		}
		state[id] = 2
		ordered = append(ordered, group)
		return nil
	}
	for id := range incoming {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func readHostSettingsIndex(ctx context.Context, tx *sql.Tx) (map[string]model.Host, error) {
	rows, err := tx.QueryContext(ctx, hostSelect)
	if err != nil {
		return nil, fmt.Errorf("导入目录时读取现有主机失败：%w", err)
	}
	defer rows.Close()
	hosts := make(map[string]model.Host)
	for rows.Next() {
		var host model.Host
		if err := scanHost(rows, &host); err != nil {
			return nil, fmt.Errorf("导入目录时读取现有主机失败：%w", err)
		}
		hosts[host.Id] = host
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("导入目录时遍历现有主机失败：%w", err)
	}
	return hosts, nil
}

func validateImportedProxyJumpChains(imported, allHosts map[string]model.Host) error {
	visited := make(map[string]bool, len(allHosts))
	visiting := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("导入的 ProxyJump 配置包含循环引用（涉及主机 %q）", id)
		}
		if visited[id] {
			return nil
		}
		if len(visiting) >= 16 {
			return errors.New("导入的 ProxyJump 链路超过 16 台主机")
		}
		host, ok := allHosts[id]
		if !ok || host.Kind != model.HostKindSSH {
			return fmt.Errorf("ProxyJump 链路引用了不存在或非 SSH 的主机 %q", id)
		}
		visiting[id] = true
		for _, jumpID := range host.ProxyJumpList {
			if err := visit(jumpID); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id, host := range imported {
		if host.Kind != model.HostKindSSH {
			continue
		}
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func samePortableHost(existing, imported model.Host) bool {
	existing.Password, imported.Password = "", ""
	existing.CredentialRef, imported.CredentialRef = "", ""
	existing.AuthProfileID, imported.AuthProfileID = "", ""
	existing.Startup, imported.Startup = "", ""
	existing.ConnectionStatus, imported.ConnectionStatus = "", ""
	existing.LastConnectedAt, imported.LastConnectedAt = time.Time{}, time.Time{}
	existing.FavoriteOrder, imported.FavoriteOrder = 0, 0
	if existing.ProxyJumpList == nil {
		existing.ProxyJumpList = []string{}
	}
	if imported.ProxyJumpList == nil {
		imported.ProxyJumpList = []string{}
	}
	return reflect.DeepEqual(existing, imported)
}
