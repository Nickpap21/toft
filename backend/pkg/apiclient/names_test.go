package apiclient

// The constants a caller of this package may already use, by v0.44.0's
// names. oapi-codegen prefixes an enum's constants with its type only when
// its values meet another enum's, so a schema added elsewhere renamed these
// once. The contract pins them with x-enum-varnames; this stops compiling
// when they move again.
var _ = []any{
	ServiceKindApp, ServiceKindCache, ServiceKindDatabase, ServiceKindTool,
	CreatedServiceKindApp, CreatedServiceKindCache, CreatedServiceKindDatabase, CreatedServiceKindTool,
	GetServicesAddressParamsTypeWeb, GetServicesAddressParamsTypeWorker,
	Image, Build, Server, Variable, Secret,
	Off, Fibonacci, Linear, Tshirt,
	ServiceContainerHealthImage, ServiceContainerHealthOff, ServiceContainerHealthCommand,
	CreatedServiceContainerHealthImage, CreatedServiceContainerHealthOff, CreatedServiceContainerHealthCommand,
	ServiceCreateInputContainerHealthImage, UpdateConfigInputContainerHealthImage,
}
