package ui

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

type sshProbeStatus uint8

const (
	sshProbeUnknown sshProbeStatus = iota
	sshProbeChecking
	sshProbeOnline
	sshProbeOffline
)

const (
	sshProbeInterval  = 30 * time.Second
	sshProbeTimeout   = 2 * time.Second
	sshProbeWorkers   = 8
	sshProbeBannerMax = 2048
)

type sshProbeCompletedMsg struct {
	generation uint64
	states     map[string]sshProbeStatus
}

type sshProbeTickMsg struct{ generation uint64 }

type sshProbeResult struct {
	hostID string
	status sshProbeStatus
}

func (m *modelState) startSSHServerProbes() tea.Cmd {
	m.sshProbeGeneration++
	generation := m.sshProbeGeneration
	m.sshProbeTimerGeneration++ // supersede a pending periodic tick while this batch is running
	hosts := m.hostsWithAuthProfiles()
	sshHosts := make([]model.Host, 0)
	m.sshProbeStates = make(map[string]sshProbeStatus)
	for _, host := range hosts {
		if host.Kind != model.HostKindSSH {
			continue
		}
		sshHosts = append(sshHosts, host)
		m.sshProbeStates[host.Id] = sshProbeChecking
	}
	if len(sshHosts) == 0 {
		m.sshProbeStates = nil
		return nil
	}
	credentials := make(map[string]string, len(m.connectionPasswords))
	for key, secret := range m.connectionPasswords {
		credentials[key] = secret
	}
	vault := m.credentials
	options := sshConnectOptions(m.config)
	options.ConnectTimeout = sshProbeTimeout
	return func() tea.Msg {
		loadedCredentials := make(map[string]bool)
		for _, host := range sshHosts {
			if len(host.ProxyJumpList) == 0 || vault == nil {
				continue
			}
			for _, authHost := range sshCredentialHosts(host, hosts) {
				if sshclient.HostCredentialKey(authHost) == sshclient.HostCredentialKey(host) || authHost.CredentialRef == "" {
					continue
				}
				key := sshclient.HostCredentialKey(authHost)
				if credentials[key] != "" || loadedCredentials[key] {
					continue
				}
				loadedCredentials[key] = true
				if secret, err := vault.Get(authHost.CredentialRef); err == nil && secret != "" {
					credentials[key] = secret
				}
			}
		}
		defer clearSSHProbeCredentials(credentials)
		return probeSSHHosts(sshHosts, hosts, credentials, options, generation)
	}
}

func (m *modelState) scheduleSSHProbeTick() tea.Cmd {
	if len(m.sshProbeStates) == 0 {
		return nil
	}
	m.sshProbeTimerGeneration++
	generation := m.sshProbeTimerGeneration
	return tea.Tick(sshProbeInterval, func(time.Time) tea.Msg {
		return sshProbeTickMsg{generation: generation}
	})
}

func probeSSHHosts(hosts, allHosts []model.Host, credentials map[string]string, options sshclient.ConnectOptions, generation uint64) sshProbeCompletedMsg {
	results := make(chan sshProbeResult, len(hosts))
	jobs := make(chan model.Host)
	workers := min(sshProbeWorkers, len(hosts))
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for host := range jobs {
				status := sshProbeOffline
				if probeSSHHost(host, allHosts, credentials, options) {
					status = sshProbeOnline
				}
				results <- sshProbeResult{hostID: host.Id, status: status}
			}
		}()
	}
	go func() {
		for _, host := range hosts {
			jobs <- host
		}
		close(jobs)
		wait.Wait()
		close(results)
	}()

	states := make(map[string]sshProbeStatus, len(hosts))
	for result := range results {
		states[result.hostID] = result.status
	}
	return sshProbeCompletedMsg{generation: generation, states: states}
}

func probeSSHHost(host model.Host, allHosts []model.Host, credentials map[string]string, options sshclient.ConnectOptions) bool {
	if len(host.ProxyJumpList) > 0 {
		return sshclient.ProbeSSHBannerThroughJumps(context.Background(), host, credentials, allHosts, options)
	}
	return probeSSHBanner(host)
}

func clearSSHProbeCredentials(credentials map[string]string) {
	for key := range credentials {
		credentials[key] = ""
		delete(credentials, key)
	}
}

func probeSSHBanner(host model.Host) bool {
	address := strings.TrimSpace(host.IP)
	if address == "" {
		return false
	}
	port := host.Port
	if port <= 0 {
		port = 22
	}
	address = net.JoinHostPort(strings.Trim(address, "[]"), strconv.Itoa(port))
	connection, err := net.DialTimeout("tcp", address, sshProbeTimeout)
	if err != nil {
		return false
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(sshProbeTimeout))
	reader := bufio.NewReader(io.LimitReader(connection, sshProbeBannerMax))
	for range 10 {
		line, err := reader.ReadString('\n')
		if strings.HasPrefix(line, "SSH-") {
			return true
		}
		if err != nil {
			return false
		}
	}
	return false
}

func sshProbeIndicator(status sshProbeStatus) string {
	switch status {
	case sshProbeChecking:
		return warningStyle.Render("●")
	case sshProbeOnline:
		return sshOnlineStyle.Render("●")
	case sshProbeOffline:
		return sshOfflineStyle.Render("●")
	default:
		return mutedStyle.Render("○")
	}
}
