package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Jeffail/benthos/v3/lib/condition"
	"github.com/Jeffail/benthos/v3/lib/config"
	"github.com/Jeffail/benthos/v3/lib/input"
	"github.com/Jeffail/benthos/v3/lib/log"
	"github.com/Jeffail/benthos/v3/lib/metrics"
	"github.com/Jeffail/benthos/v3/lib/output"
	"github.com/Jeffail/benthos/v3/lib/pipeline"
	"github.com/Jeffail/benthos/v3/lib/processor"
	"github.com/Jeffail/benthos/v3/lib/tracer"
	yaml "gopkg.in/yaml.v3"
)

//------------------------------------------------------------------------------

func create(t, path string, resBytes []byte) {
	if existing, err := ioutil.ReadFile(path); err == nil {
		if bytes.Equal(existing, resBytes) {
			fmt.Printf("Skipping '%v' at: %v\n", t, path)
			return
		}
	}
	if err := ioutil.WriteFile(path, resBytes, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("Generated '%v' at: %v\n", t, path)
}

func createYAML(t, path string, disableLint bool, sanit interface{}) {
	resBytes := []byte("# This file was auto generated by benthos_config_gen.\n")
	if disableLint {
		resBytes = append([]byte("# BENTHOS LINT DISABLE\n"), resBytes...)
	}

	var cBytes bytes.Buffer
	enc := yaml.NewEncoder(&cBytes)
	enc.SetIndent(2)
	if err := enc.Encode(sanit); err != nil {
		panic(err)
	}
	resBytes = append(resBytes, cBytes.Bytes()...)

	if existing, err := ioutil.ReadFile(path); err == nil {
		if bytes.Equal(existing, resBytes) {
			fmt.Printf("Skipping '%v' at: %v\n", t, path)
			return
		}
	}
	if err := ioutil.WriteFile(path, resBytes, 0644); err != nil {
		panic(err)
	}
	fmt.Printf("Generated '%v' config at: %v\n", t, path)
}

func envify(rootPath string, conf interface{}, paths map[string]string) (newConf interface{}) {
	genBytes, err := json.Marshal(conf)
	if err != nil {
		panic(err)
	}

	dec := json.NewDecoder(bytes.NewReader(genBytes))
	dec.UseNumber()

	var genConf interface{}
	if err = dec.Decode(&genConf); err != nil {
		panic(err)
	}

	staticlist := []string{
		"INPUT_TYPE",
		"OUTPUT_TYPE",
	}
	blacklist := []string{
		"READ_UNTIL",
		"OUTPUT_BROKER_OUTPUTS_RETRY",
		"CONDITIONAL",
		"BUFFER_MEMORY_BATCH_POLICY",
		"WHILE",
		"SWITCH",
		"PROCESS_FIELD",
		"PROCESS_MAP",
		"CHECK_FIELD",
		"FILTER",
		"DEDUPE",
		"BATCHING_CONDITION",
		"INPUT_BROKER_INPUTS_BROKER",
		"OUTPUT_BROKER_OUTPUTS_BROKER",
		"OUTPUT_BROKER_OUTPUTS_DYNAMODB",
		"LOGGER_STATIC_FIELDS",
	}
	aliases := map[string]string{
		"INPUT_BROKER_INPUTS":   "INPUT",
		"INPUT_BROKER_COPIES":   "INPUTS",
		"PIPELINE_PROCESSORS":   "PROCESSOR",
		"PIPELINE_THREADS":      "PROCESSOR_THREADS",
		"OUTPUT_BROKER_OUTPUTS": "OUTPUT",
		"OUTPUT_BROKER_PATTERN": "OUTPUTS_PATTERN",
		"OUTPUT_BROKER_COPIES":  "OUTPUTS",
	}

	var traverse func(path string, to *interface{}, from interface{})
	traverse = func(path string, to *interface{}, from interface{}) {
		if obj, isObj := from.(map[string]interface{}); isObj {
			newMap := map[string]interface{}{}
		keyIter:
			for k, v := range obj {
				newPath := path + "_" + strings.Replace(strings.ToUpper(k), "-", "_", -1)
				for _, b := range staticlist {
					if strings.Contains(newPath, b) {
						// Preserve values that hit our staticlist.
						newMap[k] = v
						continue keyIter
					}
				}
				for _, b := range blacklist {
					if strings.Contains(newPath, b) {
						// Skip values that hit our blacklist.
						continue keyIter
					}
				}
				var newVal interface{}
				traverse(newPath, &newVal, v)
				if newVal != nil {
					newMap[k] = newVal
				}
			}
			if len(newMap) > 0 {
				*to = newMap
			}
			return
		} else if len(path) == 0 {
			panic("Environment values at path root")
		}
		if array, isArray := from.([]interface{}); isArray {
			var newArray []interface{}
			for _, ele := range array {
				var newVal interface{}
				traverse(path, &newVal, ele)
				if newVal != nil {
					newArray = append(newArray, newVal)
				}
			}
			if len(newArray) > 0 {
				*to = newArray
			}
			return
		}
		for alias := range aliases {
			if strings.Contains(path, alias) {
				path = strings.Replace(path, alias, aliases[alias], 1)
			}
		}
		var valStr string
		switch t := from.(type) {
		case string:
			valStr = t
		case bool:
			if t {
				valStr = "true"
			} else {
				valStr = "false"
			}
		case json.Number:
			valStr = t.String()
		}
		paths[path] = valStr
		if len(valStr) > 0 {
			*to = "${" + path + ":" + valStr + "}"
		} else {
			*to = "${" + path + "}"
		}
	}

	traverse(rootPath, &newConf, genConf)
	return
}

func formatEnvVars(vars map[string]string) []byte {
	categories := []string{
		"HTTP", "INPUT", "BUFFER", "PROCESSOR", "OUTPUT", "LOGGER", "METRICS",
	}
	priorityVars := []string{
		"INPUTS", "PROCESSOR_THREADS", "OUTPUTS", "OUTPUTS_PATTERN",
		"INPUT_TYPE", "BUFFER_TYPE", "PROCESSOR_TYPE",
		"OUTPUT_TYPE", "METRICS_TYPE",
	}

	sortedVars := []string{}
	for k := range vars {
		sortedVars = append(sortedVars, k)
	}
	sort.Strings(sortedVars)

	var buf bytes.Buffer

	buf.WriteString(`Environment Config
==================

This document was auto generated by ` + "`benthos_config_gen`" + `.

The environment variables config ` + "[`default.yaml`](default.yaml)" + ` is an
auto generated Benthos configuration where _all_ fields can be set with
environment variables. The architecture of the config is a standard bridge
between N replicated sources, M replicated sinks and an optional buffer and
processing pipeline between them.

The original intent of this config is to be deployed within a docker image, but
as it is a standard config it can be used in other deployments.

In order to use this config simply define your env vars and point Benthos to it.
For example, to send Kafka data to RabbitMQ you can run:

` + "``` sh" + `
INPUT_TYPE=kafka_balanced \
  INPUT_KAFKA_ADDRESSES=localhost:9092 \
  INPUT_KAFKA_TOPIC=foo-topic \
  INPUT_KAFKA_CONSUMER_GROUP=foo-consumer \
  OUTPUT_TYPE=amqp \
  OUTPUT_AMQP_URL=amqp://guest:guest@localhost:5672/ \
  OUTPUT_AMQP_EXCHANGE=foo-exchange \
  OUTPUT_AMQP_EXCHANGE_TYPE=direct \
  benthos -c ./config/env/default.yaml
` + "```" + `

All variables within the config are listed in this document.

## Contents
`)

	for _, section := range categories {
		buf.WriteByte('\n')
		buf.WriteString("- [" + section + "](#" + strings.ToLower(section) + ")")
	}
	buf.WriteByte('\n')

	for _, section := range categories {
		buf.WriteString("\n")
		buf.WriteString("## " + section)
		buf.WriteString("\n\n```\n")

		catVars := []string{}

		for _, v := range priorityVars {
			if !strings.HasPrefix(v, section) {
				continue
			}
			catVars = append(catVars, v)
		}
	sortedIter:
		for _, v := range sortedVars {
			if !strings.HasPrefix(v, section) {
				continue
			}
			for _, v2 := range priorityVars {
				if v == v2 {
					continue sortedIter
				}
			}
			catVars = append(catVars, v)
		}

		vMaxLen := 0
		for _, v := range catVars {
			if len(v) > vMaxLen {
				vMaxLen = len(v)
			}
		}
		for _, v := range catVars {
			buf.WriteString(v)
			if defVal := vars[v]; len(defVal) > 0 {
				for i := len(v); i < vMaxLen; i++ {
					buf.WriteByte(' ')
				}
				buf.WriteString(" = " + defVal)
			}
			buf.WriteByte('\n')
		}
		buf.WriteString("```\n")
	}

	return buf.Bytes()
}

func createEnvConf(configsDir string) {
	inConf := input.NewConfig()
	inConf.Type = "dynamic"

	inBrokerConf := struct {
		Copies int           `json:"copies"`
		Inputs []interface{} `json:"inputs"`
	}{
		Copies: 1,
		Inputs: []interface{}{inConf},
	}

	procConf := processor.NewConfig()
	procConf.Type = "noop"

	pipeConf := pipeline.NewConfig()
	pipeConf.Processors = append(pipeConf.Processors, procConf)

	outConf := output.NewConfig()
	outConf.Type = "dynamic"

	outBrokerConf := struct {
		Copies  int           `json:"copies"`
		Pattern string        `json:"pattern"`
		Outputs []interface{} `json:"outputs"`
	}{
		Copies:  1,
		Pattern: "greedy",
		Outputs: []interface{}{outConf},
	}

	conf := config.New()
	envConf := struct {
		HTTP     interface{} `json:"http"`
		Input    interface{} `json:"input"`
		Buffer   interface{} `json:"buffer"`
		Pipeline interface{} `json:"pipeline"`
		Output   interface{} `json:"output"`
		Logger   interface{} `json:"logger"`
		Metrics  interface{} `json:"metrics"`
	}{
		HTTP: conf.HTTP,
		Input: struct {
			Type   string      `json:"type"`
			Broker interface{} `json:"broker"`
		}{
			Type:   "broker",
			Broker: inBrokerConf,
		},
		Buffer:   conf.Buffer,
		Pipeline: pipeConf,
		Output: struct {
			Type   string      `json:"type"`
			Broker interface{} `json:"broker"`
		}{
			Type:   "broker",
			Broker: outBrokerConf,
		},
		Logger:  log.NewConfig(),
		Metrics: metrics.NewConfig(),
	}

	pathsMap := map[string]string{}
	envConf.HTTP = envify("HTTP", envConf.HTTP, pathsMap)
	envConf.Input = envify("INPUT", envConf.Input, pathsMap)
	envConf.Buffer = envify("BUFFER", envConf.Buffer, pathsMap)
	envConf.Pipeline = envify("PIPELINE", envConf.Pipeline, pathsMap)
	envConf.Output = envify("OUTPUT", envConf.Output, pathsMap)
	envConf.Logger = envify("LOGGER", envConf.Logger, pathsMap)
	envConf.Metrics = envify("METRICS", envConf.Metrics, pathsMap)

	createYAML("environment file", filepath.Join(configsDir, "env", "default.yaml"), true, envConf)
	create("environment file docs", filepath.Join(configsDir, "env", "README.md"), formatEnvVars(pathsMap))
}

func main() {
	configsDir := "./config"
	flag.StringVar(&configsDir, "dir", configsDir, "The directory to write config examples")
	flag.Parse()

	// Get list of all types (both input and output).
	typeMap := map[string]struct{}{}
	for t := range input.Constructors {
		typeMap[t] = struct{}{}
	}
	for t := range output.Constructors {
		typeMap[t] = struct{}{}
	}

	// Generate configs for all types.
	for t := range typeMap {
		conf := config.New()
		conf.Input.Processors = nil
		conf.Output.Processors = nil
		conf.Pipeline.Processors = nil

		if _, exists := input.Constructors[t]; exists {
			conf.Input.Type = t
		}
		if _, exists := output.Constructors[t]; exists {
			conf.Output.Type = t
		}

		sanit, err := conf.Sanitised()
		if err != nil {
			panic(err)
		}

		createYAML(t, filepath.Join(configsDir, t+".yaml"), false, sanit)
	}

	// Create processor configs for all types.
	for t := range processor.Constructors {
		conf := config.New()
		conf.Input.Processors = nil
		conf.Output.Processors = nil

		procConf := processor.NewConfig()
		procConf.Type = t

		conf.Pipeline.Processors = append(conf.Pipeline.Processors, procConf)

		sanit, err := conf.Sanitised()
		if err != nil {
			panic(err)
		}

		createYAML(t, filepath.Join(configsDir, "processors", t+".yaml"), false, sanit)
	}

	// Create condition configs for all types.
	for t := range condition.Constructors {
		conf := config.New()
		conf.Input.Processors = nil
		conf.Output.Processors = nil

		condConf := condition.NewConfig()
		condConf.Type = t

		procConf := processor.NewConfig()
		procConf.Type = "filter_parts"
		procConf.FilterParts = processor.FilterPartsConfig{
			Config: condConf,
		}

		conf.Pipeline.Processors = append(conf.Pipeline.Processors, procConf)

		sanit, err := conf.Sanitised()
		if err != nil {
			panic(err)
		}

		createYAML(t, filepath.Join(configsDir, "conditions", t+".yaml"), false, sanit)
	}

	// Create metrics configs for all types.
	for t := range metrics.Constructors {
		conf := config.New()
		conf.Input.Processors = nil
		conf.Output.Processors = nil
		conf.Pipeline.Processors = nil

		conf.Metrics.Type = t

		sanit, err := conf.Sanitised()
		if err != nil {
			panic(err)
		}

		createYAML(t, filepath.Join(configsDir, "metrics", t+".yaml"), false, sanit)
	}

	// Create tracer configs for all types.
	for t := range tracer.Constructors {
		conf := config.New()
		conf.Input.Processors = nil
		conf.Output.Processors = nil
		conf.Pipeline.Processors = nil

		conf.Tracer.Type = t

		sanit, err := conf.Sanitised()
		if err != nil {
			panic(err)
		}

		createYAML(t, filepath.Join(configsDir, "tracers", t+".yaml"), false, sanit)
	}

	// Create Environment Vars Config
	createEnvConf(configsDir)
}

//------------------------------------------------------------------------------
