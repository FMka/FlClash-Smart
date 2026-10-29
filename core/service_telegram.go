package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// https://core.telegram.org/mtproto/samples-auth_key and mtproto-transports.
// The nonce-bound resPQ confirms MTProto access without credentials or login.
func telegramHandshake(ctx context.Context, conn net.Conn) (int64, error) {
	deadline := time.Now().Add(3 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	payload := make([]byte, 42)
	payload[0], payload[1] = 0xef, 10
	binary.LittleEndian.PutUint64(payload[10:18], uint64(time.Now().Unix())<<32)
	binary.LittleEndian.PutUint32(payload[18:22], 20)
	binary.LittleEndian.PutUint32(payload[22:26], 0xbe7e8ef1)
	if _, err := rand.Read(payload[26:42]); err != nil {
		return 0, err
	}
	n, err := conn.Write(payload)
	total := int64(n)
	if err != nil {
		return total, err
	}
	if n != len(payload) {
		return total, io.ErrShortWrite
	}
	var header [1]byte
	n, err = io.ReadFull(conn, header[:])
	total += int64(n)
	if err != nil {
		return total, err
	}
	length := int(header[0]) * 4
	if header[0] == 0x7f {
		var extended [3]byte
		n, err = io.ReadFull(conn, extended[:])
		total += int64(n)
		if err != nil {
			return total, err
		}
		length = (int(extended[0]) | int(extended[1])<<8 | int(extended[2])<<16) * 4
	}
	if length < 56 || length > 4096 {
		return total, errors.New("invalid MTProto frame length")
	}
	response := make([]byte, length)
	n, err = io.ReadFull(conn, response)
	total += int64(n)
	if err != nil {
		return total, err
	}
	if binary.LittleEndian.Uint64(response[:8]) != 0 || int(binary.LittleEndian.Uint32(response[16:20])) != length-20 || binary.LittleEndian.Uint32(response[20:24]) != 0x05162463 || !bytes.Equal(response[24:40], payload[26:42]) {
		return total, errors.New("invalid MTProto resPQ")
	}
	return total, nil
}

func checkTelegram(env serviceEnv) ServiceCheckItem {
	ctx, cancel := context.WithTimeout(env.ctx, env.timeout)
	defer cancel()
	dialer := &probeDialer{}
	if env.proxyName != "" {
		dialer.proxy = lookupProxy(env.proxyName)
		if dialer.proxy == nil {
			return ServiceCheckItem{Status: serviceFailed}
		}
	}
	start := time.Now()
	// Bootstrap DCs published in Telegram Desktop's mtproto_dc_options.cpp.
	for _, address := range []string{"149.154.167.51:443", "149.154.175.50:443"} {
		conn, err := dialer.dial(ctx, "tcp", address)
		if err != nil {
			continue
		}
		n, err := telegramHandshake(ctx, conn)
		_ = conn.Close()
		if record, ok := env.ctx.Value(smartDiagnosticKey{}).(func(int64)); ok {
			record(n)
		}
		if err == nil {
			item := ServiceCheckItem{Status: serviceAvailable, Delay: time.Since(start).Milliseconds()}
			if route := dialer.route(); route != nil {
				item.Chains = route.chains
			}
			return item
		}
	}
	if ctx.Err() != nil {
		return ServiceCheckItem{Status: serviceTimeout}
	}
	return ServiceCheckItem{Status: serviceUnavailable}
}
