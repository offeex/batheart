/*
Package cmd
Copyright © 2024 offeex

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

package cmd

import (
	"errors"
	"fmt"
	"gioui.org/x/pref/battery"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	batteryCapacityPath = "/sys/class/power_supply/BAT0/capacity"
	conserveSetPath     = "/sys/bus/platform/drivers/ideapad_acpi/VPC2004:00/conservation_mode"
)

var (
	k      = koanf.New(".")
	parser = toml.Parser()
)

type config struct {
	Threshold uint `koanf:"threshold"`
}

// not sure if this or battery.Level() is better
func getBatteryCapacity() (int, error) {
	content, err := os.ReadFile(batteryCapacityPath)
	if err != nil {
		return 0, err
	}
	capacityStr := strings.TrimSpace(string(content))
	return strconv.Atoi(capacityStr)
}

func setConservationMode(b bool) {
	var enabled []byte
	if b {
		enabled = []byte("1")
	} else {
		enabled = []byte("0")
	}

	if err := os.WriteFile(conserveSetPath, enabled, 0644); err != nil {
		log.Printf("can't change conservation mode: %v", err)
	} else {
		log.Println("Changed conservation mode to:", string(enabled))
	}
}

func inThresholdRange(capacity uint, cfg *config) bool {
	return capacity > cfg.Threshold-1 && capacity < cfg.Threshold+1
}

func runDaemon(provider *file.File, cfg *config) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)

	if err := provider.Watch(
		func(event interface{}, err error) {
			if err != nil {
				log.Printf("Error in config Watch: %v", err)
				return
			}

			log.Println("Config changed, reloading!")

			k = koanf.New(".")
			cfg = parseConfig(provider, func(err error) bool { return true })
		},
	); err != nil {
		log.Printf("Config watch error: %v", err)
		return
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer log.Println("Batheart has been shut down")

	prevCapacity := uint(0)

	log.Println("Batheart have been enabled")
	for {
		select {
		case <-sigChan:
			return
		case <-ticker.C:
			c, err := battery.Level()
			if err != nil {
				log.Printf("Error reading battery capacity: %v", err)
				continue
			}
			capacity := uint(c)

			if capacity == prevCapacity {
				continue
			}
			prevCapacity = capacity

			charging, err := battery.IsCharging()
			if err != nil {
				log.Printf("Error checking battery charging status: %v", err)
			}
			if inThresholdRange(capacity, cfg) && charging {
				ticker.Reset(1 * time.Second)
			} else if !charging {
				setConservationMode(false)
				ticker.Reset(time.Minute * 5)
				continue
			} else {
				ticker.Reset(time.Minute * 5)
			}

			setConservationMode(true)
		}
	}
}

func Execute() {
	// i don't care how shit this code is actually
	configHome, err := os.UserConfigDir()
	if err != nil {
		logCfgIssue("obtain user config dir", err)
	}

	dirPath := filepath.Join(configHome, "batheart")
	fullPath := filepath.Join(dirPath, "config.toml")
	provider := file.Provider(fullPath)

	cfg := parseConfig(provider, handleConfigError(dirPath, fullPath))
	if cfg == nil {
		fmt.Println("Using default config")
	}

	runDaemon(provider, cfg)
}

func parseConfig(
	provider *file.File,
	errHandler func(err error) bool,
) *config {
	if !errHandler(k.Load(provider, parser)) {
		return nil
	}

	var cfg config

	if err := k.Unmarshal("", &cfg); err != nil {
		logCfgIssue("parse", err)
		return nil
	}

	return &cfg
}

func handleConfigError(dirPath, fullPath string) func(err error) bool {
	return func(err error) bool {
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logCfgIssue("load", err)
				return false
			}
			if !acquireConfig(dirPath, fullPath) {
				return false
			}
		}
		return true
	}
}

func acquireConfig(dirPath string, fullPath string) bool {
	loadDefaultConfig()
	return createConfigDir(dirPath) && createConfigFile(fullPath)
}

func createConfigDir(path string) bool {
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		logCfgIssue("create config file", err)
		return false
	}
	return true
}

func createConfigFile(path string) bool {
	data, err := k.Marshal(parser)
	if err != nil {
		logCfgIssue("marshal", err)
		return false
	}

	if err := os.WriteFile(path, data, os.ModePerm); err != nil {
		logCfgIssue("write to config file", err)
		return false
	}

	return true
}

func loadDefaultConfig() {
	c := &config{
		Threshold: 80,
	}

	_ = k.Load(structs.Provider(c, "koanf"), nil)
	return
}

func logCfgIssue(action string, err error) {
	log.Fatalf("Config issue, can't %s ----> %v", action, err)
}
