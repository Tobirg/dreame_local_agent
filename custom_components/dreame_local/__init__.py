"""Dreame Local: native HA vacuum on top of dreame-local-agent's /api/v1."""

from datetime import timedelta
import logging

import aiohttp

from homeassistant.config_entries import ConfigEntry
from homeassistant.const import CONF_HOST, CONF_PORT, CONF_TOKEN, Platform
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers.aiohttp_client import async_get_clientsession
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.event import async_call_later
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed

DOMAIN = "dreame_local"
PLATFORMS = [Platform.VACUUM, Platform.SENSOR]
_LOGGER = logging.getLogger(__name__)


async def api(hass: HomeAssistant, data: dict, path: str, body: dict | None = None):
    """GET (no body) or POST to the agent. Raises HomeAssistantError with the agent's message."""
    try:
        async with async_get_clientsession(hass).request(
            "POST" if body is not None else "GET",
            f"http://{data[CONF_HOST]}:{data[CONF_PORT]}/api/v1/{path}",
            json=body,
            headers={"Authorization": f"Bearer {data[CONF_TOKEN]}"},
            timeout=aiohttp.ClientTimeout(total=40),  # agent waits up to 30 s for avacmd
        ) as resp:
            reply = await resp.json(content_type=None)
    except (aiohttp.ClientError, TimeoutError, ValueError) as err:
        raise HomeAssistantError(f"Dreame agent unreachable: {err}") from err
    if resp.status != 200:
        raise HomeAssistantError(f"Dreame agent: {reply.get('error', resp.status)}")
    return reply["result"]


async def command(hass: HomeAssistant, entry: ConfigEntry, name: str, patch: dict | None = None, **args) -> None:
    """Named command; the agent maps it to MIOT and reports robot rejection as an error."""
    await api(hass, entry.data, "command", {"name": name, **args})
    coordinator = entry.runtime_data
    if patch:
        # The agent re-polls the robot only after replying, so an immediate refresh would read the old
        # cache and flip the UI back until the next poll; show the accepted value instead.
        coordinator.async_set_updated_data({**coordinator.data, **patch})
    else:
        # The agent re-reads the robot 0.5 s and 2 s after a command; read its cache after the first.
        async def refresh(_now) -> None:
            await coordinator.async_request_refresh()

        async_call_later(hass, 1, refresh)


def device_info(entry: ConfigEntry) -> DeviceInfo:
    return DeviceInfo(
        identifiers={(DOMAIN, entry.unique_id)},
        manufacturer="Dreame",
        model=entry.data["model"],
        name=entry.title,
        sw_version=entry.data["fw"],
    )


async def async_setup_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    async def update() -> dict:
        try:
            snap = await api(hass, entry.data, "state")
        except HomeAssistantError as err:
            raise UpdateFailed(str(err)) from err
        if snap["stale"]:
            raise UpdateFailed(snap.get("error") or "robot state not read yet")
        return snap["state"]

    # The agent polls the robot itself; this only reads its cache.
    coordinator = DataUpdateCoordinator(
        hass, _LOGGER, config_entry=entry, name=DOMAIN, update_method=update, update_interval=timedelta(seconds=5)
    )
    await coordinator.async_config_entry_first_refresh()
    entry.runtime_data = coordinator
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    return True


async def async_unload_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    return await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
