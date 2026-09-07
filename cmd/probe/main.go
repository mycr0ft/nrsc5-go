package main

import (
	"fmt"
	"math"
	"net"
	"time"
)

func sendCmd(conn net.Conn, cmd byte, param uint32) {
	b := []byte{cmd, byte(param >> 24), byte(param >> 16), byte(param >> 8), byte(param)}
	conn.Write(b)
}

func bandPower(conn net.Conn, freq uint32, ms int) float64 {
	sendCmd(conn, 0x01, freq)
	time.Sleep(150 * time.Millisecond)
	buf := make([]byte, 65536)
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	var sum float64
	n := 0
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		nn, err := conn.Read(buf)
		if err != nil {
			break
		}
		for i := 0; i+1 < nn; i += 2 {
			re := float64(int(buf[i]) - 127)
			im := float64(int(buf[i+1]) - 127)
			sum += re*re + im*im
			n++
		}
	}
	if n == 0 {
		return -999
	}
	return 10 * math.Log10(sum/float64(n))
}

func main() {
	conn, err := net.Dial("tcp", "127.0.0.1:1234")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer conn.Close()
	info := make([]byte, 12)
	conn.Read(info)
	// direct sampling Q: tuner bypassed, raw RF into ADC (works up to
	// ~14.4 MHz nominally but FM leaks through the ADC's input network)
	sendCmd(conn, 0x09, 2)
	sendCmd(conn, 0x08, 1) // AGC on for max sensitivity
	sendCmd(conn, 0x02, 1488375)
	fmt.Println("direct-Q across the band:")
	for _, mhz := range []uint32{88, 89, 90, 91, 92, 94, 98, 102, 106} {
		db := bandPower(conn, mhz*1e6, 300)
		fmt.Printf("  %3d MHz: %6.1f dB\n", mhz, db)
	}
	sendCmd(conn, 0x09, 0)
}
