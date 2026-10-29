# Container networking

[Product overview](../README.md)

Commands and unlinked file paths below are relative to the repository root.

## Container IP address

To assign the app a static IP on the Docker network, append
`docker-compose.static-ip.yml` to the existing `COMPOSE_FILE` value in `.env`, preserving
any build or LAN-DNS overlays. For a published-image install without other overlays:

```dotenv
COMPOSE_FILE=docker-compose.yml:docker-compose.static-ip.yml
KY_CONTAINER_IP=172.30.0.10
KY_NETWORK_SUBNET=172.30.0.0/24
```

Choose an unused IP inside the subnet and a subnet that does not overlap your existing
networks. These settings are consumed by Compose; `KY_HOST` remains the listener address
inside the container. Without the overlay, Docker continues assigning addresses automatically.

For an existing deployment, run `docker compose down` before changing these settings,
then `docker compose up -d` to recreate the network (brief downtime; omit `-v` to retain
database volumes). For a new deployment, just run `docker compose up -d`.
