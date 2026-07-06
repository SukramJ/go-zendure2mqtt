#!/usr/bin/with-contenv bashio
# SPDX-License-Identifier: MIT
# Home Assistant add-on entrypoint for go-zendure2mqtt.
#
# Reads the user's add-on options (/data/options.json) via bashio, maps the
# scalar options onto the daemon's ZENDURE_* environment variables, writes the
# LOCAL_DEVICES list (which the env-override path cannot carry) to a generated
# config file on /data, and finally exec's the binary so it becomes PID 1 and
# receives signals directly.
set -e

bashio::log.info "Starting go-zendure2mqtt add-on..."

# --- Transport ---
export ZENDURE_CONNECTION="$(bashio::config 'connection')"
export ZENDURE_REFRESH="$(bashio::config 'refresh')"
export ZENDURE_CLOUD_APP_TOKEN="$(bashio::config 'cloud_app_token')"
export ZENDURE_CLOUD_TLS_VERIFY="$(bashio::config 'cloud_tls_verify')"

# --- MQTT ---
# Zero-config: when mqtt_server is left empty, borrow the broker the Supervisor
# already knows about (the HA MQTT integration / core-mosquitto add-on). An
# explicit mqtt_server always wins; if nothing is set and no service is offered,
# fall back to core-mosquitto:1883.
if bashio::config.has_value 'mqtt_server'; then
  export ZENDURE_MQTT_SERVER="$(bashio::config 'mqtt_server')"
  export ZENDURE_MQTT_PORT="$(bashio::config 'mqtt_port')"
  export ZENDURE_MQTT_LOGIN="$(bashio::config 'mqtt_login')"
  export ZENDURE_MQTT_PASSWORD="$(bashio::config 'mqtt_password')"
elif bashio::services.available 'mqtt'; then
  bashio::log.info "mqtt_server empty; using the Home Assistant MQTT service."
  export ZENDURE_MQTT_SERVER="$(bashio::services 'mqtt' 'host')"
  export ZENDURE_MQTT_PORT="$(bashio::services 'mqtt' 'port')"
  export ZENDURE_MQTT_LOGIN="$(bashio::services 'mqtt' 'username')"
  export ZENDURE_MQTT_PASSWORD="$(bashio::services 'mqtt' 'password')"
else
  bashio::log.warning "mqtt_server empty and no MQTT service offered; falling back to core-mosquitto:1883."
  export ZENDURE_MQTT_SERVER="core-mosquitto"
  export ZENDURE_MQTT_PORT="1883"
fi
export ZENDURE_MQTT_TOPIC="$(bashio::config 'mqtt_topic')"

# --- Home Assistant discovery ---
export ZENDURE_HASS_ENABLE="$(bashio::config 'hass_enable')"

# --- Virtual charge/discharge switches ---
export ZENDURE_CHARGE_ACTIVE_VALUE="$(bashio::config 'charge_active_value')"
export ZENDURE_DISCHARGE_ACTIVE_VALUE="$(bashio::config 'discharge_active_value')"

# --- Misc ---
export ZENDURE_LANGUAGE="$(bashio::config 'language')"
export ZENDURE_DEBUG="$(bashio::config 'debug')"

# --- Diagnostic web UI / Ingress ---
# Bind to all interfaces on 8080 so the Supervisor's Ingress proxy can reach
# the UI (the daemon's 127.0.0.1 default is unreachable from the proxy).
export ZENDURE_WEB_ENABLE="$(bashio::config 'web_enable')"
export ZENDURE_WEB_BIND="0.0.0.0:8080"

# --- LOCAL_DEVICES (a list — env overrides only carry scalars) ---
# Write a generated config file holding just the device list; the scalar
# ZENDURE_* env vars above are merged on top by the loader.
CONFIG="/data/config.yaml"
if bashio::config.has_value 'local_devices' && [ -n "$(bashio::config 'local_devices|keys')" ]; then
  echo "LOCAL_DEVICES:" > "${CONFIG}"
  for i in $(bashio::config 'local_devices|keys'); do
    sn="$(bashio::config "local_devices[${i}].sn")"
    host="$(bashio::config "local_devices[${i}].host")"
    device_name="$(bashio::config "local_devices[${i}].device_name")"
    model="$(bashio::config "local_devices[${i}].model")"
    echo "  - SN: \"${sn}\"" >> "${CONFIG}"
    echo "    HOST: \"${host}\"" >> "${CONFIG}"
    if [ -n "${device_name}" ] && [ "${device_name}" != "null" ]; then
      echo "    DEVICE_NAME: \"${device_name}\"" >> "${CONFIG}"
    fi
    if [ -n "${model}" ] && [ "${model}" != "null" ]; then
      echo "    MODEL: \"${model}\"" >> "${CONFIG}"
    fi
  done
else
  echo "LOCAL_DEVICES: []" > "${CONFIG}"
fi

bashio::log.info "Configuration prepared; connection=${ZENDURE_CONNECTION}, MQTT ${ZENDURE_MQTT_SERVER}:${ZENDURE_MQTT_PORT}."
bashio::log.info "Web UI bound to ${ZENDURE_WEB_BIND} (served via Ingress)."

# Hand off to the daemon (becomes PID 1). The catalog (zendure.yaml) is
# resolved from the WORKDIR (/app) set in the add-on Dockerfile.
exec /usr/bin/zendure2mqtt --config "${CONFIG}"
