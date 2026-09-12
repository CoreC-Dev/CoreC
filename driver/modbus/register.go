package modbus

import "github.com/CoreC-Dev/CoreC/core"

func init() {
	core.RegisterDriver("modbus-tcp", NewModbusTCPDriver)
	core.RegisterDriver("modbus-rtu", NewModbusRTUDriver)
	core.RegisterDriver("modbus-rtuovertcp", NewModbusRTUOverTCPDriver)
	core.RegisterDriver("modbus-udp", NewModbusUDPDriver)
	core.RegisterDriver("modbus-rtuoverudp", NewModbusRTUOverUDPDriver)
	core.RegisterDriver("modbus-tls", NewModbusTLSDriver)
}
