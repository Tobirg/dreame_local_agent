import voluptuous as vol

from homeassistant.config_entries import ConfigFlow
from homeassistant.const import CONF_HOST, CONF_PORT, CONF_TOKEN
from homeassistant.exceptions import HomeAssistantError

from . import DOMAIN, api

SCHEMA = vol.Schema(
    {vol.Required(CONF_HOST): str, vol.Required(CONF_PORT, default=8765): int, vol.Required(CONF_TOKEN): str}
)


class DreameLocalFlow(ConfigFlow, domain=DOMAIN):
    VERSION = 1

    async def async_step_user(self, user_input=None):
        errors = {}
        if user_input is not None:
            try:
                info = (await api(self.hass, user_input, "capabilities"))["identity"]
            except HomeAssistantError:
                errors["base"] = "cannot_connect"
            else:
                await self.async_set_unique_id(info["did"])
                self._abort_if_unique_id_configured(updates=user_input)
                return self.async_create_entry(
                    title=info["model"], data={**user_input, "model": info["model"], "fw": info["fw_ver"]}
                )
        return self.async_show_form(step_id="user", data_schema=SCHEMA, errors=errors)
