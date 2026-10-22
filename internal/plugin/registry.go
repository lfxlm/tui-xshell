// Package plugin provides the in-process extension registry used by the app.
//
// Plugins are compiled into the application in this first version. The package
// deliberately exposes a small command API so the UI can offer plugin actions
// without giving plugins access to application internals through the registry.
package plugin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const APIVersion = "v2"

// MaxCommandOutputBytes bounds the text a command can return to the UI.
const MaxCommandOutputBytes = 64 << 10

// Manifest identifies a plugin and the extension API version it expects.
type Manifest struct {
	ID         string
	Name       string
	Version    string
	APIVersion string
}

// Handler runs a plugin command and may return display text. Implementations
// should honor context cancellation and avoid sharing mutable state unsafely.
type Handler func(context.Context) (string, error)

// Command describes one action contributed by a plugin.
type Command struct {
	PluginID    string
	Name        string
	FullName    string
	Description string
	Handler     Handler
}

// Plugin contributes commands to the application during startup.
type Plugin interface {
	Manifest() Manifest
	Register(Registrar) error
}

// Registrar is the limited host API available while a plugin is registered.
type Registrar interface {
	RegisterCommand(name, description string, handler Handler) error
}

// Registry stores plugin metadata and namespaced commands.
type Registry struct {
	mu       sync.RWMutex
	plugins  map[string]Manifest
	commands map[string]Command
}

// NewRegistry creates an empty plugin registry and registers the supplied
// plugins in order.
func NewRegistry(plugins ...Plugin) (*Registry, error) {
	r := &Registry{
		plugins:  make(map[string]Manifest),
		commands: make(map[string]Command),
	}
	for _, p := range plugins {
		if err := r.Register(p); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register validates and adds a plugin. A plugin's commands are staged first,
// so a registration error does not leave a partially registered plugin.
func (r *Registry) Register(p Plugin) error {
	if p == nil {
		return fmt.Errorf("register plugin: plugin is nil")
	}

	manifest := p.Manifest()
	if !validName(manifest.ID) {
		return fmt.Errorf("register plugin: invalid plugin id %q", manifest.ID)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return fmt.Errorf("register plugin %q: name is required", manifest.ID)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return fmt.Errorf("register plugin %q: version is required", manifest.ID)
	}
	if manifest.APIVersion != APIVersion {
		return fmt.Errorf("register plugin %q: API version %q is not supported", manifest.ID, manifest.APIVersion)
	}

	staged := &stagedRegistrar{
		pluginID: manifest.ID,
		commands: make(map[string]Command),
	}
	if err := p.Register(staged); err != nil {
		return fmt.Errorf("register plugin %q: %w", manifest.ID, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[manifest.ID]; exists {
		return fmt.Errorf("register plugin: duplicate plugin id %q", manifest.ID)
	}
	for localName, command := range staged.commands {
		if _, exists := r.commands[command.FullName]; exists {
			return fmt.Errorf("register plugin %q: duplicate command %q", manifest.ID, localName)
		}
	}

	r.plugins[manifest.ID] = manifest
	for _, command := range staged.commands {
		r.commands[command.FullName] = command
	}
	return nil
}

// Commands returns a stable, sorted snapshot of registered commands.
func (r *Registry) Commands() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	commands := make([]Command, 0, len(r.commands))
	for _, command := range r.commands {
		commands = append(commands, command)
	}
	sort.Slice(commands, func(i, j int) bool {
		return commands[i].FullName < commands[j].FullName
	})
	return commands
}

// Run executes a command by its fully qualified name, for example
// "sftp.refresh". Handlers are called after the registry lock is released.
// Returned output is bounded because it is displayed in the terminal UI.
func (r *Registry) Run(ctx context.Context, fullName string) (output string, err error) {
	r.mu.RLock()
	command, exists := r.commands[fullName]
	r.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("plugin command %q not found", fullName)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			output = ""
			err = fmt.Errorf("run plugin command %q: panic: %v", fullName, recovered)
		}
	}()
	output, err = command.Handler(ctx)
	if err != nil {
		return "", fmt.Errorf("run plugin command %q: %w", fullName, err)
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if len(output) > MaxCommandOutputBytes {
		output = strings.ToValidUTF8(output[:MaxCommandOutputBytes], "�") + "\n…（输出已截断）"
	}
	if !utf8.ValidString(output) {
		output = strings.ToValidUTF8(output, "�")
	}
	return output, nil
}

type stagedRegistrar struct {
	pluginID string
	commands map[string]Command
}

func (r *stagedRegistrar) RegisterCommand(name, description string, handler Handler) error {
	if !validName(name) {
		return fmt.Errorf("invalid command name %q", name)
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("command %q: description is required", name)
	}
	if handler == nil {
		return fmt.Errorf("command %q: handler is required", name)
	}
	if _, exists := r.commands[name]; exists {
		return fmt.Errorf("duplicate command name %q", name)
	}

	r.commands[name] = Command{
		PluginID:    r.pluginID,
		Name:        name,
		FullName:    r.pluginID + "." + name,
		Description: description,
		Handler:     handler,
	}
	return nil
}

func validName(value string) bool {
	if value == "" || !isAlphaNumeric(value[0]) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func isAlphaNumeric(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
}
