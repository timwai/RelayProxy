package protocol

// ServerExitDeviceID is the reserved exit identifier for the Relay process
// itself. Device IDs are generated with the dev_ prefix, so this cannot collide
// with an enrolled device.
const ServerExitDeviceID = "server"
