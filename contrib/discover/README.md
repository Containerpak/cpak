# KDE Discover backend

The cpak backend uses `cpak discover` as its only runtime interface. The CLI
verifies every catalog entry before exposing it to Discover, and installation
is enabled only when the signed entry binds an immutable manifest.

Discover does not install its backend headers or export `Discover::Common` as
a public CMake package. Build this directory inside the Discover source tree:

1. Copy or link `CpakBackend` to `libdiscover/backends/CpakBackend`.
2. Add `add_subdirectory(CpakBackend)` to `libdiscover/backends/CMakeLists.txt`.
3. Build and install Discover using its documented build process.

The plugin depends on Qt and KDE Frameworks. The cpak binary does not.
