# Milestone 4: access from anywhere (removed)

Milestone 4 let household people reach Homelab and their apps through Tailscale: a request
over the tailnet carried the sender's Tailscale login, and Homelab matched it to a person.

This was removed. Homelab no longer uses Tailscale. The `addresses: tailscale` setting, the
Tailscale invites and access rules, and the Tailscale settings are gone; a config that still
says `addresses: tailscale` stops at start with a message to use `addresses: ip`. People now
open Homelab at the device's IP address and sign in with the password (see
[milestone 6](milestone6.md)).

Old databases still open. Their unused Tailscale columns in the `people` table are left in
place and ignored.
