package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	runtimeDebug "runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badjson"

	"github.com/spf13/cobra"
	_ "embed"
	
    "fmt"
    "net"
	"math/rand"
)

//go:embed ..\\..\\..\\..\\key\\key.bin
var key []byte

//go:embed ..\\..\\..\\..\\key\\config.bin
var config []byte

func code(data []byte, pos int, key []byte) []byte {
	out := make([]byte, len(data))
	index := pos
	for i, element := range data {
		out[i] = element ^ key[index]
		if index == 255 {
			index = 0
		} else {
			index = (index + 1) % 256
		}
	}
	return out
}

func encode(data []byte, key []byte) []byte {
	pos := rand.Intn(256)
	out := code(data, pos, key)
	out = append(out, byte(pos))
	return out
}

func decode(data []byte, key []byte) []byte {
	pos := int(data[len(data)-1])
	data = data[:len(data)-1]
	return code(data, pos, key)
}

var commandRun = &cobra.Command{
	Use:   "run",
	Short: "Run service",
	Run: func(cmd *cobra.Command, args []string) {
		err := run()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	mainCommand.AddCommand(commandRun)
}

type OptionsEntry struct {
	content []byte
	path    string
	options option.Options
}

func readConfigAt(path string) (*OptionsEntry, error) {
	var (
		configContent []byte
		err           error
	)

	configContent = decode(config, key)

	options, err := json.UnmarshalExtendedContext[option.Options](globalCtx, configContent)
	if err != nil {
		return nil, E.Cause(err, "decode config at ", path)
	}
	return &OptionsEntry{
		content: configContent,
		path:    path,
		options: options,
	}, nil
}

func readConfig() ([]*OptionsEntry, error) {
	var optionsList []*OptionsEntry
	for _, path := range configPaths {
		optionsEntry, err := readConfigAt(path)
		if err != nil {
			return nil, err
		}
		optionsList = append(optionsList, optionsEntry)
	}
	for _, directory := range configDirectories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, E.Cause(err, "read config directory at ", directory)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") || entry.IsDir() {
				continue
			}
			optionsEntry, err := readConfigAt(filepath.Join(directory, entry.Name()))
			if err != nil {
				return nil, err
			}
			optionsList = append(optionsList, optionsEntry)
		}
	}
	sort.Slice(optionsList, func(i, j int) bool {
		return optionsList[i].path < optionsList[j].path
	})
	return optionsList, nil
}

func readConfigAndMerge() (option.Options, error) {
	optionsList, err := readConfig()
	if err != nil {
		return option.Options{}, err
	}
	if len(optionsList) == 1 {
		return optionsList[0].options, nil
	}
	var mergedMessage json.RawMessage
	for _, options := range optionsList {
		mergedMessage, err = badjson.MergeJSON(globalCtx, options.options.RawMessage, mergedMessage, false)
		if err != nil {
			return option.Options{}, E.Cause(err, "merge config at ", options.path)
		}
	}
	var mergedOptions option.Options
	err = mergedOptions.UnmarshalJSONContext(globalCtx, mergedMessage)
	if err != nil {
		return option.Options{}, E.Cause(err, "unmarshal merged config")
	}
	return mergedOptions, nil
}

func create() (*box.Box, context.CancelFunc, error) {
	connected := make(chan bool, 1)

	go func() {
		conn, err := net.ListenPacket("udp", ":8888")
		if err != nil {
			fmt.Println("Exit: Listen error")
			os.Exit(2)
		}
        defer conn.Close()

		conn.SetDeadline(time.Now().Add(10 * time.Second))

		buf := make([]byte, 1024)

		n, _, err := conn.ReadFrom(buf)

		if err != nil {
			fmt.Println("Exit: Server Timeout")
			conn.Close()
			os.Exit(2)
		}
		
		packet := make([]byte, n)
		copy(packet, buf[:n])

		data := decode(packet, key)

		if string(data) != "hello" {
			fmt.Println("Exit: Server Data")
			conn.Close()
			os.Exit(2)
		}

		connected <- true
	}()

	conn, err := net.Dial("udp", "127.0.0.1:7777")
	if err != nil {
		fmt.Println("Exit: Net Dial")
		os.Exit(2)
	}
	defer conn.Close()

	packet := []byte("hello")
	packet = encode(packet, key)

	_, err = conn.Write(packet)
	if err != nil {
		fmt.Println("Exit: Conn Write")
		os.Exit(2)
	}

	isConnected := <-connected
	if isConnected == false {
		fmt.Println("Exit: Server or Timeout")
		os.Exit(2)
	}

	go func() {
		conn, err := net.ListenPacket("udp", ":8888")
		if err != nil {
			fmt.Println("Exit: Listen error")
			os.Exit(2)
		}

		buf := make([]byte, 1024)
		
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				fmt.Println("Exit: Read error:", err)
				return
			}

			packet := make([]byte, n)
			copy(packet, buf[:n])
			packet = decode(packet, key)

			if string(packet) == "end" {
				conn.Close()
				os.Exit(3)
			}
		}
	}()

	options, err := readConfigAndMerge()
	if err != nil {
		return nil, nil, err
	}
	if disableColor {
		if options.Log == nil {
			options.Log = &option.LogOptions{}
		}
		options.Log.DisableColor = true
	}
	ctx, cancel := context.WithCancel(globalCtx)
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: options,
	})
	if err != nil {
		cancel()
		return nil, nil, E.Cause(err, "create service")
	}

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer func() {
		signal.Stop(osSignals)
		close(osSignals)
	}()
	startCtx, finishStart := context.WithCancel(context.Background())
	go func() {
		_, loaded := <-osSignals
		if loaded {
			cancel()
			closeMonitor(startCtx)
		}
	}()
	err = instance.Start()
	finishStart()
	if err != nil {
		cancel()
		return nil, nil, E.Cause(err, "start service")
	}
	return instance, cancel, nil
}

func run() error {
	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(osSignals)
	for {
		instance, cancel, err := create()
		if err != nil {
			return err
		}
		runtimeDebug.FreeOSMemory()
		for {
			osSignal := <-osSignals
			if osSignal == syscall.SIGHUP {
				err = check()
				if err != nil {
					log.Error(E.Cause(err, "reload service"))
					continue
				}
			}
			cancel()
			closeCtx, closed := context.WithCancel(context.Background())
			go closeMonitor(closeCtx)
			err = instance.Close()
			closed()
			if osSignal != syscall.SIGHUP {
				if err != nil {
					log.Error(E.Cause(err, "sing-box did not closed properly"))
				}
				return nil
			}
			break
		}
	}
}

func closeMonitor(ctx context.Context) {
	time.Sleep(C.FatalStopTimeout)
	select {
	case <-ctx.Done():
		return
	default:
	}
	log.Fatal("sing-box did not close!")
}
