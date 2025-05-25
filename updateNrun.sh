#!/bin/bash

cd ~/Downloads/highAvailability/
git pull --force
go build -o ha-webserver

iface=$(ip -o link show | awk '!/lo/ {print $2; exit}' | sed 's/://')

if [ -z "$iface" ]; then
    echo "Error: Could not retrieve interface name."
    exit 1
fi


ipv4=$(ip -4 a show $iface | grep -oP '(?<=inet\s)\d+(\.\d+){3}')

if [ -z "$ipv4" ]; then
    echo "Error: Could not retrieve IPv4 address."
    exit 1
fi


if [ "$ipv4" = "192.168.1.30" ]; then
    peeripv4="192.168.1.40"
else
    peeripv4="192.168.1.30"
fi

sudo env NODE_ID=node1 OWN_PORT=9090 PEER_ADDR=$peeripv4:9090 VIP_MASK=192.168.1.100/24 INTERFACE=$iface HTTP_PORT=8080 ./ha-webserver