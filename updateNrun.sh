#!/bin/bash

cd ~/Downloads/highAvailability/
git pull
go build -o ha-webserver

iface=$(ip -o link show | awk '!/lo/ {print $2; exit}' | sed 's/://')
ipv4=$(ip -4 a show $iface | grep -oP '(?<=inet\s)\d+(\.\d+){3}')


if [$ipv4 -eq "192.168.1.30"];then
    peeripv4="192.168.1.40"
else
    peeripv4="192.168.1.30"
fi

sudo env NODE_ID=node1 OWN_PORT=9090 PEER_ADDR=$peeripv4:9090 VIP=192.168.1.100/24 INTERFACE=$iface HTTP_PORT=8080 ./ha-webserver