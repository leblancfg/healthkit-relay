---
title: Privacy policy
description: What the Health Relay iPhone app reads, where it sends it, and how to delete it.
---

# Privacy policy

*Effective October 25, 2026. Applies to the Health Relay app for iPhone and to the
healthkit-relay server software.*

Health Relay is published by François Leblanc ("we"). It copies your Apple Health data to a
server that **you** set up and run, such as the healthkit-relay program on a computer at
home, a NAS or a cloud server you rent. We do not run that server, and we receive none of
your data.

## Data the app reads

With your permission, given in Apple Health's own permission sheet, the app reads the
Health data types you allow, for example:

- activity: steps, distance, flights climbed, active and resting energy, exercise and
  stand time, workouts with their routes;
- heart: heart rate, heart rate variability, resting and walking heart rate, ECG recordings,
  irregular rhythm notifications, blood oxygen;
- body measurements and vital signs: weight, height, body fat, temperature, blood pressure,
  respiratory rate;
- sleep, mindfulness and State of Mind;
- nutrition, hydration, medications you log in Health, and cycle tracking;
- hearing, vision prescriptions, and your characteristics (date of birth, biological sex,
  blood type).

You pick the types in Apple Health's sheet, and can change your choice at any time in
**Settings › Health › Data Access & Devices › Health Relay**. The app only reads: it leaves
your Health data as it is. The App Store version of the app reads no clinical health records.

## Where the data goes

- **To your server only.** The app sends the data it reads to the server address you set
  up, over HTTPS (or plain HTTP to an address on your own local network, if you choose
  one). The server then keeps it in files on your own computer.
- **Kept between you and your server.** The app connects to your server and to nothing
  else; its code is all its own, without analytics, advertising, tracking or
  crash-reporting libraries. We, and any third party, have no access to your data, and it
  is used for no advertising, marketing or data mining.
- **On your iPhone.** While a batch waits to be sent, the app keeps it in its own storage,
  protected by iOS until you first unlock the iPhone, and left out of iCloud and computer
  backups. Each batch is deleted once sent. The server address and the app's token are in the
  iPhone's Keychain, for this device only.
- **Diagnostic log.** The app keeps a small log of sync steps and counts on the iPhone (no
  Health values). It leaves the iPhone only if you share it yourself from **Advanced ›
  Share Diagnostic Log**.

In App Store terms, the app collects no data: nothing leaves your iPhone except to the
server you set up.

## The demo server

So that Apple's App Review can try the app, we run a demonstration server, reachable only
with a setup code given to App Review. Data sent to it is erased automatically every hour
and is never used for anything. It is not part of the app's normal use.

## Keeping and deleting your data

Your server keeps your data for as long as you keep it there. You control it completely:

- **Delete it from your server:** in the app, **Advanced › Erase Server Data and Start Over**
  has your server permanently delete everything your iPhone sent. You can also delete the
  server's files yourself.
- **Stop syncing:** **Advanced › Disconnect** in the app, or turn off Health Relay's access
  in the Health settings above.
- **Remove the app:** deleting the app removes its token, pending batches and log from your
  iPhone.

The app works without an account: you set it up with your own server's setup code, and
everything about you stays on your iPhone and your server.

## Children

The app is not directed at children and collects no data from anyone.

## Changes

If this policy changes, the new version is posted on this page with a new effective date.

## Contact

Questions about privacy: [healthrelay@leblancfg.com](mailto:healthrelay@leblancfg.com).
