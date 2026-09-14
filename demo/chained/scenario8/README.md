# Scenario 8: Auto-Discovery Two-Level Cascade

Same topology as scenario2, but using **topology auto-discovery** instead of
explicit topic configuration.

## Topology

```
PLC ──modbus──▶ CoreC-A ──MQTT(auto)──▶ CoreC-B ──MQTT(cloud/...)──▶ subscriber
(mock-plc)      (collector)              (relay)                       (observe)
```

## What's Different from Scenario 2

| Field | Scenario 2 (manual) | Scenario 8 (auto-discovery) |
|:---|:---|:---|
| A's `topic-template` | `"edgeA/{{.Driver}}/{{.Tag}}"` | **omitted** → auto `"topo/edge-A/data/..."` |
| B's `data-topic` | `"edgeA/#"` | **omitted** → auto-discovered from A's heartbeat |
| B's `parser` | `{ type: default }` | **omitted** → auto-set to `default` |
| A's `rules` | `forward-all → mqtt` | **omitted** → auto-added |
| B's `rules` | `forward → mqtt-out` | explicit (business logic) |
| B's `topic-template` | `"cloud/{{.Driver}}/{{.Tag}}"` | explicit (subscriber expects this) |

## How Auto-Discovery Works

1. Both nodes broadcast heartbeats to `corec/_discovery/{node-id}` on the broker
2. A's heartbeat says: "I'm edge-A, I publish to `topo/edge-A/data/#`"
3. B's heartbeat says: "I'm relay-B, I subscribe to `edge-A`"
4. B sees A's heartbeat → auto-creates a transport with `data-topic: "topo/edge-A/data/#"`
5. Data flows: A → broker → B → broker → subscriber

## Key Principle: Explicit Overrides Auto

- Fields you write are used as-is (e.g., B's `topic-template: "cloud/..."`)
- Fields you omit are auto-generated
- Existing configs without a `node:` section work unchanged

## Run

```bash
docker compose up --build
```

The subscriber should see:
```
cloud/plc/temperature {"driver":"plc","name":"temperature","value":25.5,...}
cloud/plc/humidity    {"driver":"plc","name":"humidity","value":60,...}
```
