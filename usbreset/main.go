// usbreset envoie un vrai reset USB (USBDEVFS_RESET) à un seul périphérique, à condition
// qu'il ait l'identité attendue. Usage:
//
//	usbreset /sys/bus/usb/devices/3-1 1b1c:0c3f
//
// Équivaut à débrancher puis rebrancher ce périphérique, sans toucher au câble.
// Si le chemin désigne un autre appareil (câble déplacé par exemple), il refuse d'agir.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const usbdevfsReset = 0x5514 // _IO('U', 20)

func readString(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func fail(args ...any) {
	fmt.Fprintln(os.Stderr, args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: usbreset /sys/bus/usb/devices/<bus>-<port> <vendor>:<produit>")
		os.Exit(2)
	}
	dir, want := os.Args[1], strings.ToLower(os.Args[2])

	vendor, err := readString(dir + "/idVendor")
	if err != nil {
		fail("idVendor:", err)
	}
	product, err := readString(dir + "/idProduct")
	if err != nil {
		fail("idProduct:", err)
	}
	if got := strings.ToLower(vendor + ":" + product); got != want {
		fail("refus:", dir, "est", got, "et non", want)
	}

	busnum, err := readString(dir + "/busnum")
	if err != nil {
		fail("busnum:", err)
	}
	devnum, err := readString(dir + "/devnum")
	if err != nil {
		fail("devnum:", err)
	}
	bus, err1 := strconv.Atoi(busnum)
	dev, err2 := strconv.Atoi(devnum)
	if err1 != nil || err2 != nil {
		fail("numéros de bus/appareil illisibles:", busnum, devnum)
	}

	node := fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, dev)
	f, err := os.OpenFile(node, os.O_WRONLY, 0)
	if err != nil {
		fail("ouverture", node+":", err)
	}
	defer f.Close()
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), usbdevfsReset, 0); e != 0 {
		fail("reset", node+":", e)
	}
	fmt.Println("reset USB envoyé à", node, "("+want+")")
}
