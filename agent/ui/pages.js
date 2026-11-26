// Settings pages, in the order of the Dreame app. A page lists its settings explicitly (items); `match` is the
// fallback for a setting the list does not name (a new model or plugin version): the first page whose
// pattern fits takes it. A page shows only while at least one of its rows or actions is visible, so a model
// without a station has no station page. `home` is a panel that shows the page next to its related controls.
export const pages=[
 {title:"Настроить уборку", home:"tune-settings", match:"^$", // the map's «Настроить» sheet, as in the app
  items:["cleaning_mode","wetness_level","self_clean_frequency","self_clean_value","cleaning_route"]},
 {title:"Уборка", match:"^(power_saving|cleaning_mode|mop_humidity|wetness_level|cleangenius(_mode)?|cleaning_route|customized_cleaning|tight_mopping|auto_recleaning|resume_cleaning|max_suction_power|ultra_clean_mode|mopping_type)$",
  items:["resume_cleaning","customized_cleaning","tight_mopping","max_suction_power","ultra_clean_mode","mopping_type","auto_recleaning","cleangenius","cleangenius_mode","mop_humidity","power_saving"]},
 {title:"Ковры", match:"carpet",
  items:["carpet_boost","carpet_recognition","intensive_carpet_cleaning","clean_carpets_first","side_brush_carpet_rotate","lift_chassis_on_carpet","close_roller_cover_on_carpet","carpet_sensitivity","carpet_cleaning"]},
 {title:"MopExtend и боковой вылет", match:"^(side_reach|mop_extend(_frequency)?|gap_cleaning_extension|mopping_under_furnitures|wider_corner_coverage|mop_pad_swing)$",
  items:["side_reach","mop_extend","gap_cleaning_extension","mopping_under_furnitures","wider_corner_coverage","mop_pad_swing","mop_extend_frequency"]},
 {title:"Ножки: преодоление препятствий", match:"^obstacle_crossing$",
  items:["obstacle_crossing"]},
 {title:"Чистка пола", match:"^(collision_avoidance|floor_direction_cleaning|stain_avoidance|auto_mount_mop|pet_focused_cleaning|dynamic_obstacle_cleaning|pressurized_cleaning|mop_pressure|ai_bit_large_particles_boost)$",
  items:["auto_mount_mop","ai_bit_large_particles_boost","collision_avoidance","stain_avoidance","floor_direction_cleaning","pet_focused_cleaning","dynamic_obstacle_cleaning","pressurized_cleaning","mop_pressure"]},
 {title:"Базовая станция", home:"station-settings", match:"^(?!dnd_).*(wash|dry|uv_|dust|empty|hair|detergent|water|self_clean|mop_temperature|scraper|low_lying|change_mop|mop_cleaning_remainder)",
  items:["auto_dust_collecting","self_clean","water_electrolysis","auto_water_refilling","auto_drying","auto_add_detergent","mopping_with_detergent","auto_change_mop","smart_drying","hot_washing","uv_sterilization","smart_mop_washing","silent_drying","hair_compression","dust_bag_drying","auto_empty_frequency","drying_time","mop_wash_level","mop_temperature","low_lying_area_frequency","scraper_frequency","auto_rewashing","water_temperature","mop_cleaning_remainder","auto_empty_area"]},
 {title:"Препятствия и ИИ", match:"^ai_|obstacle|crossing|fill_light|intelligent_recognition|human_follow|lds",
  items:["obstacle_avoidance","intelligent_recognition","ai_bit_ai_obstacle_detection","ai_bit_ai_obstacle_image_upload","ai_bit_ai_obstacle_picture","ai_json_ai_human_detection","ai_bit_ai_furniture_detection","ai_bit_ai_fluid_detection","ai_bit_fuzzy_obstacle_detection","fill_light","human_follow","active_suspension_crossing","lds_state","auto_lds_coverage","ai_json_ai_obstacle_detection","ai_json_ai_obstacle_image_upload","ai_json_ai_furniture_detection","ai_json_ai_fluid_detection"]},
 {title:"Питомцы", home:"pet-settings", match:"^ai_(bit|json)_(ai_)?pet_",
  items:["ai_bit_ai_pet_detection","ai_bit_pet_picture","ai_bit_pet_focused_detection","ai_bit_ai_pet_avoidance","ai_json_ai_pet_detection"]},
 {title:"Голос", home:"voice-settings", match:"voice",
  items:["voice_assistant","voice_dreame_gpt","streaming_voice_prompt","voice_assistant_language"]},
 {title:"Что ограничивает ночной режим", home:"dnd-settings", match:"^dnd_(disable|reduce)",
  items:["dnd_disable_resume_cleaning","dnd_disable_auto_empty","dnd_reduce_volume"]},
 {title:"Не беспокоить", match:"^dnd",
  items:["dnd_tasks","dnd_start","dnd_end","dnd"]},
 {title:"Прочее", match:".",
  items:["child_lock","multi_floor_map","map_saving","off_peak_charging","auto_charging","ring_light_always_on","battery_charge_level","timezone"]},
];
