package protocol

// FileShare RPC runs only over device-authenticated Relay streams. File data
// follows one success response as raw bytes so downloads need no JSON buffering.
const (
	FileShareListShares = "list_shares"
	FileShareListDir    = "list_dir"
	FileShareDownload   = "download"
	FileShareUpload     = "upload"
)

type FileSharePeer struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Online   bool   `json:"online"`
}

type FileSharePeersResponse struct {
	Peers []FileSharePeer `json:"peers"`
	Error string          `json:"error,omitempty"`
}

type FileShareRequest struct {
	Action  string `json:"action"`
	ShareID string `json:"shareId,omitempty"`
	Path    string `json:"path,omitempty"`
	Size    int64  `json:"size,omitempty"`
}

type FileShareDescriptor struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Permission string `json:"permission"`
}

type FileShareEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
}

type FileShareResponse struct {
	Success bool                  `json:"success"`
	Error   string                `json:"error,omitempty"`
	Shares  []FileShareDescriptor `json:"shares,omitempty"`
	Entries []FileShareEntry      `json:"entries,omitempty"`
	Size    int64                 `json:"size,omitempty"`
}

type FileShareCandidate struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Online   bool   `json:"online"`
}

type FileShareCandidatesResponse struct {
	Devices []FileShareCandidate `json:"devices"`
	Error   string `json:"error,omitempty"`
}
