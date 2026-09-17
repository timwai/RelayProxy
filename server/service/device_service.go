package service

import "relayproxy/server/repository"

// DeviceService remains the API composition boundary for device features.
// Enrollment and authorization mutations are intentionally implemented by the
// repository transaction methods; there are no pairing-code or token methods.
type DeviceService struct {
	db *repository.DB
}

func NewDeviceService(db *repository.DB) *DeviceService {
	return &DeviceService{db: db}
}
