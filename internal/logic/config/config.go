package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"tui-ssh-workbench/internal/consts"
	"tui-ssh-workbench/internal/infra/terminalog"
	"tui-ssh-workbench/internal/model"
	"tui-ssh-workbench/internal/service"

	"github.com/gogf/gf/v2/os/gcmd"
	"github.com/gogf/gf/v2/os/gfile"
)

type sConfigManager struct {
	config model.Config
}

func init() {
	service.RegisterConfigManager(&sConfigManager{})
}

func (s *sConfigManager) ParseConfig(ctx context.Context, parser *gcmd.Parser) error {
	requestedPath := strings.TrimSpace(parser.GetOpt("config", "").String())
	configPath := requestedPath
	if configPath == "" {
		configPath = consts.DefaultConfigPath
	}
	if !gfile.Exists(configPath) {
		if requestedPath == "" {
			s.config = model.DefaultConfig()
			return nil
		}
		return fmt.Errorf("配置文件不存在：%s", configPath)
	}
	loaded, err := Load(configPath)
	if err != nil {
		return err
	}
	s.config = loaded
	return nil
}

func (s *sConfigManager) Config() model.Config {
	if s.config.SSH.ConnectTimeoutSeconds == 0 {
		return model.DefaultConfig()
	}
	return s.config
}

func Load(path string) (model.Config, error) {
	config := model.DefaultConfig()
	if _, err := toml.DecodeFile(path, &config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return model.Config{}, fmt.Errorf("读取配置文件 %s 失败：%w", path, err)
		}
		return model.Config{}, fmt.Errorf("解析配置文件 %s 失败：%w", path, err)
	}
	if err := config.Validate(); err != nil {
		return model.Config{}, err
	}
	if err := terminalog.ValidatePatterns(config.Logging.RedactPatterns); err != nil {
		return model.Config{}, fmt.Errorf("日志脱敏规则无效：%w", err)
	}
	return config, nil
}
