// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command zendure2mqtt-util is a diagnostic CLI for the bridge: inspect a
// device's report, write a property, test the cloud login, and validate
// the property catalog — all without touching the MQTT broker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/discovery"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/version"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/cloud"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/local"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "discover":
		err = cmdDiscover(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "resolve":
		err = cmdResolve(os.Args[2:])
	case "set":
		err = cmdSet(os.Args[2:])
	case "cloud-login":
		err = cmdCloudLogin(os.Args[2:])
	case "catalog-check":
		err = cmdCatalogCheck(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version.String())
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `zendure2mqtt-util — diagnostic CLI

Usage:
  zendure2mqtt-util discover     [--service _zendure._tcp] [--timeout 5s]
  zendure2mqtt-util report       --host <ip>
  zendure2mqtt-util resolve      --host <ip> [--catalog zendure.yaml] [--lang en|de]
  zendure2mqtt-util set          --host <ip> --sn <sn> --prop <key> --value <v>
  zendure2mqtt-util cloud-login  --token <app-token>
  zendure2mqtt-util catalog-check [--catalog zendure.yaml]
  zendure2mqtt-util version
`)
}

// cmdDiscover browses the LAN for Zendure mDNS services and prints them.
func cmdDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	service := fs.String("service", discovery.ZendureService, "mDNS service type")
	timeout := fs.Duration("timeout", 5*time.Second, "browse duration")
	_ = fs.Parse(args)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+time.Second)
	defer cancel()
	svcs, err := discovery.Browse(ctx, *service, *timeout)
	if err != nil {
		return err
	}
	if len(svcs) == 0 {
		fmt.Printf("no %s services found\n", *service)
		return nil
	}
	for _, s := range svcs {
		addrs := make([]string, 0, len(s.Addrs))
		for _, ip := range s.Addrs {
			addrs = append(addrs, ip.String())
		}
		fmt.Printf("  %-28s %s:%d  addrs=[%s]  txt=%v\n",
			s.Instance, s.Host, s.Port, strings.Join(addrs, ","), s.TXT)
	}
	return nil
}

// cmdReport fetches and pretty-prints a device's /properties/report.
func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	host := fs.String("host", "", "device IP or hostname")
	_ = fs.Parse(args)
	if *host == "" {
		return fmt.Errorf("--host is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rep, err := local.FetchReport(ctx, &http.Client{Timeout: local.DefaultHTTPTimeout}, *host)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
	return nil
}

// cmdResolve fetches a report and prints the catalog-resolved points
// (scaling, value maps, packData expansion) — the exact values the bridge
// would publish. Useful for verifying the catalog against a real device.
func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	host := fs.String("host", "", "device IP or hostname")
	catalogPath := fs.String("catalog", "zendure.yaml", "path to the property catalog")
	lang := fs.String("lang", "en", "label language (en|de)")
	_ = fs.Parse(args)
	if *host == "" {
		return fmt.Errorf("--host is required")
	}
	cat, err := catalog.LoadFile(*catalogPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rep, err := local.FetchReport(ctx, &http.Client{Timeout: local.DefaultHTTPTimeout}, *host)
	if err != nil {
		return err
	}
	points := process.Resolve(rep, cat, *lang)
	sort.Slice(points, func(i, j int) bool {
		if points[i].Group != points[j].Group {
			return points[i].Group < points[j].Group
		}
		return points[i].Topic < points[j].Topic
	})
	fmt.Printf("device %s (%s): %d points\n", rep.SN, rep.Product, len(points))
	for _, p := range points {
		suffix := ""
		if p.PackSN != "" {
			suffix = "  [" + p.PackSN + "]"
		}
		if p.Entry == nil {
			suffix += "  (unmapped)"
		}
		fmt.Printf("  %-8s %-22s = %v%s\n", p.Group, p.Topic, p.Value, suffix)
	}
	return nil
}

// cmdSet writes a single property to a device.
func cmdSet(args []string) error {
	fs := flag.NewFlagSet("set", flag.ExitOnError)
	host := fs.String("host", "", "device IP or hostname")
	sn := fs.String("sn", "", "device serial number")
	prop := fs.String("prop", "", "property key (e.g. acMode)")
	value := fs.String("value", "", "value to write")
	_ = fs.Parse(args)
	if *host == "" || *sn == "" || *prop == "" || *value == "" {
		return fmt.Errorf("--host, --sn, --prop and --value are all required")
	}
	var v any = *value
	if i, err := strconv.Atoi(*value); err == nil {
		v = i
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := local.WriteProperties(ctx, &http.Client{Timeout: local.DefaultHTTPTimeout}, *host, *sn, map[string]any{*prop: v}); err != nil {
		return err
	}
	fmt.Printf("wrote %s=%v to %s\n", *prop, v, *sn)
	return nil
}

// cmdCloudLogin decodes the app token, logs in, and prints the result.
func cmdCloudLogin(args []string) error {
	fs := flag.NewFlagSet("cloud-login", flag.ExitOnError)
	token := fs.String("token", "", "Zendure app token (base64)")
	_ = fs.Parse(args)
	if *token == "" {
		return fmt.Errorf("--token is required")
	}
	apiURL, appKey, err := cloud.DecodeToken(*token)
	if err != nil {
		return err
	}
	fmt.Printf("api_url: %s\nappKey:  %s…\n", apiURL, mask(appKey))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := cloud.Login(ctx, &http.Client{Timeout: 20 * time.Second}, apiURL, appKey)
	if err != nil {
		return err
	}
	host, port := res.MQTT.HostPort()
	fmt.Printf("cloud broker: %s:%d (user %s)\n", host, port, res.MQTT.Username)
	fmt.Printf("devices (%d):\n", len(res.DeviceList))
	for _, d := range res.DeviceList {
		fmt.Printf("  - %s  sn=%s  productKey=%s  model=%s\n", d.DeviceName, d.SnNumber, d.ProductKey, d.ProductModel)
	}
	return nil
}

// cmdCatalogCheck loads the catalog and prints a summary.
func cmdCatalogCheck(args []string) error {
	fs := flag.NewFlagSet("catalog-check", flag.ExitOnError)
	path := fs.String("catalog", "zendure.yaml", "path to the property catalog")
	_ = fs.Parse(args)
	cat, err := catalog.LoadFile(*path)
	if err != nil {
		return err
	}
	entries := cat.Entries()
	fmt.Printf("%s: %d entries\n", *path, len(entries))
	for i := range entries {
		e := &entries[i]
		w := ""
		if e.Writable {
			w = " [writable]"
		}
		fmt.Printf("  %-22s → %s/%s  %s%s\n", e.Property, e.Group, e.TopicLeaf(), e.Platform, w)
	}
	return nil
}

// mask redacts all but the first few characters of a secret.
func mask(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4]
}
