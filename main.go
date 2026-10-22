package main

import (
	_ "tui-ssh-workbench/internal/logic"

	"github.com/gogf/gf/v2/os/gctx"

	"tui-ssh-workbench/internal/cmd"
)

func main() {
	cmd.Main.Run(gctx.GetInitCtx())
}
