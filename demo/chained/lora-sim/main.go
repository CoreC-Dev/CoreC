// lora-sim: simulates a third-party LoRa gateway publishing non-CoreC-format
// JSON to MQTT. Used in scenario 1 to demonstrate parser: jsonpath.
//
// Publishes to topic "lora/{dev_id}/up" every 2 seconds with a payload like:
//   {"dev_id":"sensor-01","temp":23.4,"hum":55.0,"ts":1700000000}
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	broker := os.Getenv("MQTT_BROKER")
	if broker == "" {
		broker = "tcp://broker:1883"
	}
	devID := os.Getenv("DEV_ID")
	if devID == "" {
		devID = "sensor-01"
	}

	opts := mqtt.NewClientOptions().AddBroker(broker)
	opts.SetClientID("lora-sim-" + devID)
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		fmt.Fprintf(os.Stderr, "lora-sim: connect failed: %v\n", token.Error())
		os.Exit(1)
	}
	defer client.Disconnect(250)

	fmt.Printf("lora-sim: connected to %s, publishing as %s\n", broker, devID)

	ticker := time.NewTicker(2 * time.Second)
	t := 0.0
	for range ticker.C {
		payload := map[string]any{
			"dev_id": devID,
			"temp":   math.Round((22.0+3.0*math.Sin(t))*10) / 10,
			"hum":    math.Round((55.0+10.0*math.Cos(t))*10) / 10,
			"ts":     time.Now().Unix(),
		}
		body, _ := json.Marshal(payload)
		topic := fmt.Sprintf("lora/%s/up", devID)
		client.Publish(topic, 0, false, body)
		fmt.Printf("lora-sim: %s -> %s\n", topic, string(body))
		t += 0.3
	}
}
