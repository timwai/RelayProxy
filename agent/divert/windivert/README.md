# WinDivert embedded runtime

`WinDivert-2.2.2-A.zip` is the unmodified official release archive, including
its license. Windows amd64 Agent builds embed it with `go:embed`; other targets
do not include this asset.

Source: https://github.com/basil00/WinDivert/tree/v2.2.2

SHA256: `63cb41763bb4b20f600b6de04e991a9c2be73279e317d4d82f237b150c5f3f15`

Refresh and verify the asset from the repository root:

```sh
go run ./scripts/fetch-windivert.go -embed-archive agent/divert/windivert/WinDivert-2.2.2-A.zip
```

The Agent extracts the DLL, signed driver, license and source notice only when
transparent interception is started. An external runtime beside the EXE (or
in its `windivert` directory) takes precedence, allowing DLL replacement.
