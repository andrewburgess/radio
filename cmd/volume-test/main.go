//go:build pi

// volume-test continuously reads the MCP3008 ADC over SPI and prints the raw
// 10-bit value alongside the derived volume percentage. Use it to verify SPI
// wiring, confirm the correct channel, and check the full pot sweep before
// trusting the main volume code.
//
// Usage:
//
//	CGO_ENABLED=1 go run -tags pi ./cmd/volume-test/ [spi-device] [channel]
//
// Defaults: SPI0.0, channel 0.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

const mcp3008Max = 1023

func main() {
	if _, err := host.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "periph init: %v\n", err)
		os.Exit(1)
	}

	spiDev := "SPI0.0"
	if len(os.Args) > 1 {
		spiDev = os.Args[1]
	}

	channel := 0
	if len(os.Args) > 2 {
		n, err := strconv.Atoi(os.Args[2])
		if err != nil || n < 0 || n > 7 {
			fmt.Fprintf(os.Stderr, "channel must be 0-7, got %q\n", os.Args[2])
			os.Exit(1)
		}
		channel = n
	}

	port, err := spireg.Open(spiDev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open SPI device %q: %v\n", spiDev, err)
		os.Exit(1)
	}
	defer port.Close()

	// MCP3008: 1 MHz, SPI Mode 0, 8 bits per word.
	conn, err := port.Connect(physic.MegaHertz, spi.Mode0, 8)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect SPI: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Reading MCP3008 on %s channel %d - turn the pot and watch the values.\n\n", spiDev, channel)
	fmt.Printf("%-10s  %-6s\n", "raw (0-1023)", "pct %")
	fmt.Println("----------  ------")

	const windowSize = 5
	window := make([]int, 0, windowSize)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	tick := 0
	for range ticker.C {
		tick++
		raw, err := readADC(conn, channel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tick %d read error: %v\n", tick, err)
			continue
		}

		if len(window) >= windowSize {
			window = window[1:]
		}
		window = append(window, raw)

		var sum int
		for _, v := range window {
			sum += v
		}
		avg := sum / len(window)
		pct := avg * 100 / mcp3008Max

		fmt.Printf("\rtick=%-4d  raw=%-10d  pct=%-6d", tick, avg, pct)
		os.Stdout.Sync()
	}
}

// readADC performs a standard 3-byte MCP3008 SPI transaction for single-ended
// mode and returns the 10-bit result (0-1023).
func readADC(conn spi.Conn, channel int) (int, error) {
	ch := byte(channel & 0x07)
	tx := []byte{0x01, (0x80 | ch<<4), 0x00}
	rx := make([]byte, 3)
	if err := conn.Tx(tx, rx); err != nil {
		return 0, err
	}
	result := int(rx[1]&0x03)<<8 | int(rx[2])
	return result, nil
}
