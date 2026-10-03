# hev-socks5-tunnel Android runtime

RelayProxy builds the JNI runtime from the upstream `2.18.0` source release at
APK build time. The build script verifies the archive SHA-256 before compiling
both Android ABIs with NDK 27.2.12479018 and API 26. The upstream Android.mk
sets 16 KB ELF page alignment.

- Source: https://github.com/heiher/hev-socks5-tunnel
- Release archive: `hev-socks5-tunnel-2.18.0.tar.xz`
- SHA-256: `93b3b33127436b4eab669798f1d50e019008585a27880df8cbdeffe5e70cb665`
- License: MIT, see [LICENSE](LICENSE)

The generated native libraries are written under the Android app's ignored
`build/generated/hev-jniLibs` directory and are not checked into the repository.
