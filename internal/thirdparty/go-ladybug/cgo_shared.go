package lbug

//go:generate bash ../../../scripts/fetch-lbug.sh

/*
// liblbug is fetched by scripts/fetch-lbug.sh (not committed).
//
// linux + darwin: STATIC — liblbug.a is linked in (only the archive
// lives in lib/static/<os>-<arch>/, so `-llbug` resolves to it) for a
// self-contained binary with no runtime lib to ship. The C++ runtime is
// linked too: libc++ on darwin (system, always present); libstdc++ +
// libgcc statically on linux so the binary doesn't need them at runtime.
//
// windows: DYNAMIC — lbug's windows release is MSVC-built (its C++
// runtime is MSVCP140/VCRUNTIME140), which cannot be statically linked
// into a mingw binary. The .exe links directly against lbug_shared.dll
// (mingw ld reads the DLL's clean C ABI export table via -l:<file>, so
// no import lib / gendef is needed) and ships the DLL — plus the VC++
// runtime — alongside the .exe at runtime.
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/lib/static/darwin-amd64 -llbug -lc++
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/lib/static/darwin-arm64 -llbug -lc++
// libstdc++ is wrapped in -Wl,-Bstatic/-Bdynamic (NOT -static-libstdc++):
// cgo links the final binary with the C driver (CC=*-linux-gnu-gcc),
// which never auto-appends libstdc++, so -static-libstdc++ would be a
// no-op and the explicit -lstdc++ would resolve to libstdc++.so.6 at
// runtime — defeating the self-contained goal. -Bstatic forces the .a.
// libm/dl/pthread stay dynamic (system libs always present); libgcc is
// statically linked via -static-libgcc (honoured — gcc auto-adds -lgcc).
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/lib/static/linux-amd64 -llbug -Wl,-Bstatic -lstdc++ -Wl,-Bdynamic -lm -ldl -lpthread -static-libgcc
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/lib/static/linux-arm64 -llbug -Wl,-Bstatic -lstdc++ -Wl,-Bdynamic -lm -ldl -lpthread -static-libgcc
#cgo windows LDFLAGS: -L${SRCDIR}/lib/dynamic/windows -l:lbug_shared.dll
#include "lbug.h"
*/
import "C"
