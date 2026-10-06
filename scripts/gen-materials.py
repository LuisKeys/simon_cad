#!/usr/bin/env python3
"""Regenerates the scalar-only built-in material library under materials/.
Textures (CC0) are added later by hand/script and listed in each material.json."""
import json, os

ROOT = os.path.join(os.path.dirname(__file__), "..", "materials")
LIC = {"source": "Simon CAD", "license": "CC0-1.0", "url": ""}
# id, name, category, color, metalness, roughness, opacity, tags
M = [
 ("brushed_aluminum","Brushed Aluminum","Metals","#BFC2C4",1.0,0.35,None,["metal","aluminum","brushed","silver","robotics"]),
 ("polished_aluminum","Polished Aluminum","Metals","#D0D3D6",1.0,0.12,None,["metal","aluminum","polished","shiny","silver"]),
 ("anodized_black_aluminum","Black Anodized Aluminum","Metals","#2A2C2F",0.9,0.4,None,["metal","aluminum","anodized","black","robotics"]),
 ("anodized_blue_aluminum","Blue Anodized Aluminum","Metals","#2F5D9E",0.9,0.4,None,["metal","aluminum","anodized","blue"]),
 ("stainless_steel","Stainless Steel","Metals","#B8BBBE",1.0,0.3,None,["metal","steel","stainless","silver","shaft"]),
 ("polished_steel","Polished Steel","Metals","#C8CBCE",1.0,0.1,None,["metal","steel","polished","shiny"]),
 ("raw_steel","Raw Steel","Metals","#7C7F82",0.9,0.55,None,["metal","steel","raw","gray","shaft"]),
 ("black_steel","Black Steel","Metals","#2B2D2F",0.85,0.5,None,["metal","steel","black","oxide","fastener"]),
 ("brass","Brass","Metals","#C9A24B",1.0,0.3,None,["metal","brass","gold","yellow","bushing"]),
 ("copper","Copper","Metals","#B87333",1.0,0.28,None,["metal","copper","orange"]),
 ("titanium","Titanium","Metals","#8E8F93",1.0,0.4,None,["metal","titanium","gray"]),
 ("black_abs","Black ABS","Plastics","#1E1F21",0.0,0.5,None,["plastic","abs","black","printed","housing"]),
 ("white_abs","White ABS","Plastics","#EDEDEA",0.0,0.5,None,["plastic","abs","white","housing"]),
 ("black_pla","Black PLA","Plastics","#1B1C1E",0.0,0.6,None,["plastic","pla","black","printed"]),
 ("white_pla","White PLA","Plastics","#F0F0EE",0.0,0.6,None,["plastic","pla","white","printed"]),
 ("gray_pla","Gray PLA","Plastics","#8A8D91",0.0,0.6,None,["plastic","pla","gray","printed"]),
 ("petg_black","Black PETG","Plastics","#222426",0.0,0.35,None,["plastic","petg","black","printed"]),
 ("petg","PETG","Plastics","#9CC3D5",0.0,0.3,None,["plastic","petg","printed","blue"]),
 ("nylon_white","White Nylon","Plastics","#E6E3DA",0.0,0.7,None,["plastic","nylon","white","gear"]),
 ("transparent_plastic","Transparent Plastic","Plastics","#DDE9EE",0.0,0.15,0.35,["plastic","transparent","clear","cover"]),
 ("black_rubber","Black Rubber","Rubber","#161616",0.0,0.85,None,["rubber","black","grip","seal","wheel"]),
 ("gray_rubber","Gray Rubber","Rubber","#55575A",0.0,0.85,None,["rubber","gray","grip"]),
 ("carbon_fiber","Carbon Fiber","Carbon Fiber","#1A1B1D",0.2,0.3,None,["carbon","fiber","composite","black","panel","robotics"]),
 ("clear_glass","Clear Glass","Glass","#E8F2F4",0.0,0.03,0.25,["glass","clear","transparent","window"]),
 ("tinted_glass","Tinted Glass","Glass","#3B4A50",0.0,0.05,0.5,["glass","tinted","dark","transparent"]),
 ("green_pcb","Green PCB","Electronics","#0F6B3A",0.0,0.45,None,["electronics","pcb","green","board","circuit"]),
 ("black_pcb","Black PCB","Electronics","#151617",0.0,0.4,None,["electronics","pcb","black","board","circuit"]),
 ("oak_wood","Oak Wood","Wood","#B58B55",0.0,0.65,None,["wood","oak","natural","brown"]),
 ("plywood","Plywood","Wood","#C9A675",0.0,0.7,None,["wood","plywood","light","laser"]),
 ("generic_gray","Generic Gray","Generic","#9AA0A6",0.25,0.45,None,["generic","default","gray"]),
 ("generic_white","Generic White","Generic","#F2F2F2",0.0,0.5,None,["generic","white","matte"]),
]
for id_,name,cat,col,met,rough,op,tags in M:
    d = os.path.join(ROOT, id_); os.makedirs(d, exist_ok=True)
    j = {"id":id_,"name":name,"category":cat,"tags":tags,"baseColor":col,"metalness":met,"roughness":rough}
    if op is not None: j["opacity"] = op
    j["textureScale"] = 20 if cat == "Metals" else 1
    j["license"] = LIC
    with open(os.path.join(d,"material.json"),"w") as f:
        json.dump(j,f,indent=2); f.write("\n")
print(len(M),"materials")
