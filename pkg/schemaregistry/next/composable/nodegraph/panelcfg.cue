// Code generated - EDITING IS FUTILE. DO NOT EDIT.
//
// Generated by:
//     public/app/plugins/gen.go
// Using jennies:
//     PluginSchemaRegistryJenny
//
// Run 'make gen-cue' from repository root to regenerate.

import "github.com/grafana/kindsys"

kindsys.Composable & {
	maturity:        "experimental"
	name:            "NodeGraph" + "PanelCfg"
	schemaInterface: "PanelCfg"
	lineage: {
		seqs: [{
			schemas: [{
				ArcOption: {
					// Field from which to get the value. Values should be less than 1, representing fraction of a circle.
					field?: string
					// The color of the arc.
					color?: string
				} @cuetsy(kind="interface")
				NodeOptions: {
					// Unit for the main stat to override what ever is set in the data frame.
					mainStatUnit?: string
					// Unit for the secondary stat to override what ever is set in the data frame.
					secondaryStatUnit?: string
					// Define which fields are shown as part of the node arc (colored circle around the node).
					arcs?: [...ArcOption]
				}
				EdgeOptions: {
					// Unit for the main stat to override what ever is set in the data frame.
					mainStatUnit?: string
					// Unit for the secondary stat to override what ever is set in the data frame.
					secondaryStatUnit?: string
				}
				PanelOptions: {
					nodes?: NodeOptions
					edges?: EdgeOptions
				} @cuetsy(kind="interface")
			}]
		}]
		name: "NodeGraph" + "PanelCfg"
	}
}