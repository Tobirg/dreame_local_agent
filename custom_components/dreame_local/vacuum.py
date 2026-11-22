from homeassistant.components.vacuum import StateVacuumEntity, VacuumActivity, VacuumEntityFeature
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from . import command, device_info

FAN_SPEEDS = ["quiet", "standard", "strong", "turbo"]  # agent "suction" value = index


async def async_setup_entry(hass, entry, async_add_entities):
    async_add_entities([DreameVacuum(entry)])


class DreameVacuum(CoordinatorEntity, StateVacuumEntity):
    _attr_has_entity_name = True
    _attr_name = None
    _attr_fan_speed_list = FAN_SPEEDS
    _attr_supported_features = (
        VacuumEntityFeature.STATE
        | VacuumEntityFeature.START
        | VacuumEntityFeature.PAUSE
        | VacuumEntityFeature.STOP
        | VacuumEntityFeature.RETURN_HOME
        | VacuumEntityFeature.LOCATE
        | VacuumEntityFeature.FAN_SPEED
    )

    def __init__(self, entry):
        super().__init__(entry.runtime_data)
        self._entry = entry
        self._attr_unique_id = entry.unique_id
        self._attr_device_info = device_info(entry)

    @property
    def activity(self):
        value = self.coordinator.data.get("activity")
        return VacuumActivity(value) if value else None

    @property
    def fan_speed(self):
        level = self.coordinator.data.get("suction")
        return FAN_SPEEDS[level] if isinstance(level, int) and 0 <= level < len(FAN_SPEEDS) else None

    async def async_start(self):
        await command(self.hass, self._entry, "start")

    async def async_pause(self):
        await command(self.hass, self._entry, "pause")

    async def async_stop(self, **kwargs):
        await command(self.hass, self._entry, "stop")

    async def async_return_to_base(self, **kwargs):
        await command(self.hass, self._entry, "dock")

    async def async_locate(self, **kwargs):
        await command(self.hass, self._entry, "locate")

    async def async_set_fan_speed(self, fan_speed, **kwargs):
        if fan_speed not in FAN_SPEEDS:
            raise HomeAssistantError(f"Unknown fan speed: {fan_speed}")
        level = FAN_SPEEDS.index(fan_speed)
        await command(self.hass, self._entry, "set_suction", patch={"suction": level}, value=level)
