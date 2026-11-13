The release job puts the signed `toft-agent-linux-amd64`, `toft-agent-linux-arm64`
and their `.sig` files here before building the backend image (D-106). A local
build leaves this directory empty and the image carries unsigned agents, which
an installed agent refuses as an upgrade.
