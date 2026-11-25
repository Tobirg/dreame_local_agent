// Action buttons that belong to a settings page (station jobs). `command` is the agent's command name from
// /api/v1/capabilities: the agent maps it to a fixed siid/aiid, the panel never sends a raw action.
// A button shows unless the robot's capabilities rule the command out (`available: false`), and is disabled
// while another command runs. `danger` and `confirm` ask first; `page` is a title from pages.js.
export const actions=[
 {command:'empty',label:'Выгрузить пыль',page:'Базовая станция'},
 {command:'wash_mop',label:'Мыть салфетки',page:'Базовая станция'},
 {command:'dry_start',label:'Начать сушку',page:'Базовая станция'},
 {command:'dry_stop',label:'Остановить сушку',page:'Базовая станция'},
 {command:'tank_drain',label:'Слить бак робота',page:'Базовая станция',confirm:'Слить воду из бака робота на станцию?'},
 {command:'station_drain',label:'Слить воду станции',page:'Базовая станция',confirm:'Слить воду станции в канализацию?'},
 {command:'station_clean',label:'Чистка поддона',page:'Базовая станция',confirm:'Запустить чистку поддона станции?'},
 {command:'dock_self_repair',label:'Саморемонт станции',page:'Базовая станция',confirm:'Запустить саморемонт станции? Около 5–10 минут, робот должен стоять на станции.'},
 {command:'dust_bag_dry_start',label:'Сушка мешка для пыли',page:'Базовая станция'},
 {command:'dust_bag_dry_stop',label:'Остановить сушку мешка',page:'Базовая станция'},
];
