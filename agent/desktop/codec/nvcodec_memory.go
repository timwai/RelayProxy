package codec

type NVCodecVideoMemoryInfo struct {
	Identity                     NVCodecValidationIdentity `json:"identity"`
	BudgetBytes                  uint64                    `json:"budgetBytes"`
	CurrentUsageBytes            uint64                    `json:"currentUsageBytes"`
	AvailableForReservationBytes uint64                    `json:"availableForReservationBytes"`
	CurrentReservationBytes      uint64                    `json:"currentReservationBytes"`
	SampledAtUnixMs              int64                     `json:"sampledAtUnixMs"`
}
