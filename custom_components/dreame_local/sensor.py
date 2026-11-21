# Battery is a separate sensor: HA deprecated battery on the vacuum entity itself.
from homeassistant.components.sensor import SensorDeviceClass, SensorEntity, SensorStateClass
from homeassistant.const import PERCENTAGE
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from . import device_info


async def async_setup_entry(hass, entry, async_add_entities):
    async_add_entities([DreameBattery(entry)])


class DreameBattery(CoordinatorEntity, SensorEntity):
    _attr_has_entity_name = True
    _attr_device_class = SensorDeviceClass.BATTERY
    _attr_native_unit_of_measurement = PERCENTAGE
    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(self, entry):
        super().__init__(entry.runtime_data)
        self._attr_unique_id = f"{entry.unique_id}_battery"
        self._attr_device_info = device_info(entry)

    @property
    def native_value(self):
        return self.coordinator.data.get("battery")
