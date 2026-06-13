package broadcast

import (
	"log"
	"os"
)

// debugEnabled gates verbose diagnostic logging behind CRIRADIO_DEBUG=1 so it
// stays silent in normal operation. Temporary diagnostic aid.
var debugEnabled = os.Getenv("CRIRADIO_DEBUG") != ""

func dbg(format string, a ...any) {
	if debugEnabled {
		log.Printf("DBG "+format, a...)
	}
}
