package consts

type ConnectionStatus string

const (
	ConnectionStatusActive   ConnectionStatus = "active"     // 可连接
	ConnectionStatusInactive ConnectionStatus = "inactive"   // 无法连接
	ConnectionConnecting     ConnectionStatus = "connecting" // 连接中
)
