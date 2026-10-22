package sqlite

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"tui-ssh-workbench/internal/model"
)

// ListPortForwardRules returns the saved SSH forwarding rules for a host.
func (s *Store) ListPortForwardRules(ctx context.Context, hostID string) ([]model.PortForwardRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, host_id, kind, listen_address, remote_address
		FROM port_forward_rules WHERE host_id = ?
		ORDER BY kind, listen_address, remote_address, id
	`, hostID)
	if err != nil {
		return nil, fmt.Errorf("list port forwarding rules: %w", err)
	}
	defer rows.Close()

	rules := make([]model.PortForwardRule, 0)
	for rows.Next() {
		var rule model.PortForwardRule
		if err := rows.Scan(&rule.Id, &rule.HostId, &rule.Kind, &rule.ListenAddress, &rule.RemoteAddress); err != nil {
			return nil, fmt.Errorf("scan port forwarding rule: %w", err)
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate port forwarding rules: %w", err)
	}
	return rules, nil
}

// SavePortForwardRule stores an auto-start rule. Repeated saves of the same
// host/kind/listener/target tuple return the existing row instead of creating
// duplicates.
func (s *Store) SavePortForwardRule(ctx context.Context, rule model.PortForwardRule) (model.PortForwardRule, error) {
	if err := normalizePortForwardRule(&rule); err != nil {
		return model.PortForwardRule{}, err
	}
	host, err := s.GetHost(ctx, rule.HostId)
	if err != nil {
		return model.PortForwardRule{}, fmt.Errorf("find host for port forwarding rule: %w", err)
	}
	if host.Kind != model.HostKindSSH {
		return model.PortForwardRule{}, errors.New("port forwarding rules require an SSH host")
	}
	if rule.Id == "" {
		rule.Id, err = newID()
		if err != nil {
			return model.PortForwardRule{}, fmt.Errorf("generate port forwarding rule ID: %w", err)
		}
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO port_forward_rules (id, host_id, kind, listen_address, remote_address)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(host_id, kind, listen_address, remote_address) DO NOTHING
	`, rule.Id, rule.HostId, rule.Kind, rule.ListenAddress, rule.RemoteAddress)
	if err != nil {
		return model.PortForwardRule{}, fmt.Errorf("save port forwarding rule: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT id FROM port_forward_rules
		WHERE host_id = ? AND kind = ? AND listen_address = ? AND remote_address = ?
	`, rule.HostId, rule.Kind, rule.ListenAddress, rule.RemoteAddress).Scan(&rule.Id); err != nil {
		return model.PortForwardRule{}, fmt.Errorf("read saved port forwarding rule: %w", err)
	}
	return rule, nil
}

// DeletePortForwardRule removes a saved rule without affecting any active
// forwarding listener.
func (s *Store) DeletePortForwardRule(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM port_forward_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete port forwarding rule: %w", err)
	}
	return requireAffected(result, "delete port forwarding rule")
}

func normalizePortForwardRule(rule *model.PortForwardRule) error {
	rule.HostId = strings.TrimSpace(rule.HostId)
	rule.ListenAddress = strings.TrimSpace(rule.ListenAddress)
	rule.RemoteAddress = strings.TrimSpace(rule.RemoteAddress)
	if rule.HostId == "" {
		return errors.New("port forwarding host ID is required")
	}
	if err := validateForwardAddress(rule.ListenAddress); err != nil {
		return fmt.Errorf("port forwarding listen address is invalid: %w", err)
	}
	switch rule.Kind {
	case model.PortForwardLocalTCP, model.PortForwardRemoteTCP:
		if err := validateForwardAddress(rule.RemoteAddress); err != nil {
			return fmt.Errorf("port forwarding target address is invalid: %w", err)
		}
	case model.PortForwardSOCKS5:
		if rule.RemoteAddress != "" {
			return errors.New("SOCKS5 forwarding rules cannot have a fixed target address")
		}
		host, _, _ := net.SplitHostPort(rule.ListenAddress)
		if strings.ToLower(host) != "localhost" {
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				return errors.New("unauthenticated SOCKS5 listeners must bind to loopback")
			}
		}
	default:
		return errors.New("port forwarding kind must be local_tcp, remote_tcp, or socks5")
	}
	return nil
}

func validateForwardAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("host cannot be empty")
	}
	if strings.ContainsAny(host, "\r\n") || strings.ContainsAny(portText, "\r\n") {
		return errors.New("address contains a line break")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}
