//go:build pi

// toggle-test continuously reads the two GPIO pins wired to the AM/AFC/FM
// toggle switch and prints the current mode. Use it to verify wiring and
// confirm correct pin assignments before trusting the main toggle code.
//
// Usage:
//
//	go run -tags pi ./cmd/toggle-test/ [pinA] [pinB]
//
// Defaults: GPIO17, GPIO18
//
// Wiring:
//
//	Row 1 → GND
//	Row 2 → pinA (pull-up)
//	Row 3 → pinB (pull-up)
//	Row 4 → GND
package main

import (
	"fmt"
	"os"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

func main() {
	if _, err := host.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "periph init: %v\n", err)
		os.Exit(1)
	}

	pinNameA := "GPIO17"
	pinNameB := "GPIO18"
	if len(os.Args) > 1 {
		pinNameA = os.Args[1]
	}
	if len(os.Args) > 2 {
		pinNameB = os.Args[2]
	}

	pinA := gpioreg.ByName(pinNameA)
	if pinA == nil {
		fmt.Fprintf(os.Stderr, "GPIO pin %q not found\n", pinNameA)
		os.Exit(1)
	}
	pinB := gpioreg.ByName(pinNameB)
	if pinB == nil {
		fmt.Fprintf(os.Stderr, "GPIO pin %q not found\n", pinNameB)
		os.Exit(1)
	}

	for _, p := range []gpio.PinIn{pinA, pinB} {
		if err := p.In(gpio.PullUp, gpio.NoEdge); err != nil {
			fmt.Fprintf(os.Stderr, "configure pin: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("Pins: A=%s  B=%s\n", pinNameA, pinNameB)
	fmt.Printf("Wiring: Row1=GND  Row2=A  Row3=B  Row4=GND\n\n")
	fmt.Printf("%-12s  %-6s  %-6s\n", "mode", "A", "B")
	fmt.Println("------------  ------  ------")

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		a := pinA.Read()
		b := pinB.Read()
		mode := readMode(a, b)
		fmt.Printf("\r%-12s  %-6s  %-6s", mode, levelStr(a), levelStr(b))
	}
}

func readMode(a, b gpio.Level) string {
	if a == gpio.Low {
		return "AM/podcast"
	}
	if b == gpio.Low {
		return "FM/music"
	}
	return "AFC/speaker"
}

func levelStr(l gpio.Level) string {
	if l == gpio.High {
		return "HIGH"
	}
	return "LOW"
}
