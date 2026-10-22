# Local VT fork

Based on `github.com/charmbracelet/x/vt` v0.1.0. The upstream license is in
`LICENSE`.

Local changes:

- Make emulator closed-state checks atomic so a blocked response reader can be
  safely released during pane shutdown.
- Add a mutex-protected `SafeEmulator.Close` method.
