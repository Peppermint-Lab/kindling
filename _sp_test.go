package main

import (
	"fmt"
	"net"
)

func main() {
	for _, s := range []string{"0.0.0.0:32768", "127.0.0.1:32768", "[::]:32768", ":::32768"} {
		h, p, e := net.SplitHostPort(s)
		fmt.Printf("%q -> host=%q port=%q err=%v\n", s, h, p, e)
	}
}
