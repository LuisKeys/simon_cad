package features

// KindParams marks nodes that hold design parameters and produce no geometry. Other
// features drive their own parameters from them through expressions ("vehicle.wheelbase"),
// so editing one parameter regenerates every dependent feature.
const KindParams = "parameters"

// ParamsType is the node type of a design-parameter set.
const ParamsType = "parameters"

func init() {
	register(&Feature{Type: ParamsType, Kind: KindParams, KeepsInputs: true})
}
