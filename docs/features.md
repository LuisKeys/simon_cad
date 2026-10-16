# Simon CAD — Feature list

Lista completa de funcionalidades, agrupada por área. Para el detalle exacto de cada herramienta
(esquemas JSON) ver [tools.md](tools.md); para la arquitectura ver [architecture.md](architecture.md).

Estado: **M0** (geometría) y **M1** (motor agéntico headless) implementados; **M2** (CAD visual)
implementado y verificado en navegador. Las fases 3–7 (robótica, análisis, diseño generativo,
ensamblajes) no están iniciadas. Ver el [README](../README.md#status).

## Índice

1. [Núcleo y arquitectura](#1-núcleo-y-arquitectura)
2. [Modelado de sólidos](#2-modelado-de-sólidos)
3. [Sketches y perfiles 2D](#3-sketches-y-perfiles-2d)
4. [Edición de geometría](#4-edición-de-geometría)
5. [Transformaciones y organización de objetos](#5-transformaciones-y-organización-de-objetos)
6. [Parámetros y paramétrica](#6-parámetros-y-paramétrica)
7. [Componentes reutilizables](#7-componentes-reutilizables)
8. [Apariencia y materiales](#8-apariencia-y-materiales)
9. [Medición, unidades, grid y snapping](#9-medición-unidades-grid-y-snapping)
10. [Historial, checkpoints y replay](#10-historial-checkpoints-y-replay)
11. [Agente de IA](#11-agente-de-ia)
12. [Imagen a CAD y documentos](#12-imagen-a-cad-y-documentos)
13. [Refinamiento visual](#13-refinamiento-visual)
14. [Interfaz de usuario](#14-interfaz-de-usuario)
15. [Viewport y renderizado](#15-viewport-y-renderizado)
16. [Atajos de teclado y mouse](#16-atajos-de-teclado-y-mouse)
17. [CLI](#17-cli)
18. [Proyectos, persistencia y exportación](#18-proyectos-persistencia-y-exportación)
19. [Configuración y proveedores de modelo](#19-configuración-y-proveedores-de-modelo)
20. [Calidad, seguridad y pruebas](#20-calidad-seguridad-y-pruebas)
21. [Ayuda integrada](#21-ayuda-integrada)
22. [Simulación de impresión 3D](#22-simulación-de-impresión-3d)
23. [Paths, sweeps, vectorización, relieves, sellos y texto](#23-paths-sweeps-vectorización-relieves-sellos-y-texto)
24. [Fuera de alcance / pendiente](#24-fuera-de-alcance--pendiente)

---

## 1. Núcleo y arquitectura

- **Agentic-first:** CLI, agente y UI comparten la misma ruta *tool → command → ChangeSet*; no existe
  ruta de mutación exclusiva de la IA ni de la UI.
- **CAD-IR semántico** como fuente de verdad; el B-Rep de OpenCascade (OCCT 7.8.1) es derivado.
- **Motor de reconstrucción** con caché por clave de contenido (solo se reconstruye lo que cambió).
- **Puente C++/Go** hacia OCCT (C ABI), sin fugas de memoria (verificado con valgrind).
- **Pipeline de validación por llamada:** herramienta conocida → decodificación JSON estricta →
  validación de esquema → validación semántica → plan de comandos.
- **ChangeSets** propuestos → validados → confirmados (commit) o rechazados; cada commit crea un
  checkpoint semántico.
- **Bus de eventos** y registro append-only.
- **Selectores respaldados por topología** (`face`, `edge`, `vertex`, `body`).

## 2. Modelado de sólidos

Primitivas:

- Caja / bloque (`create_box`)
- Cilindro (`create_cylinder`)
- Cono / tronco de cono (`create_cone`)
- Esfera (`create_sphere`)
- Toro (`create_torus`)

## 3. Sketches y perfiles 2D

- Sketch planar editable y reutilizable (`create_sketch`): líneas, arcos, círculos, rectángulos,
  polilíneas.
- Extrusión de regiones de un sketch (`extrude_sketch`), añadiendo o cortando material.
- Perfil rápido + extrusión en un paso (`extrude_profile`), modo `add` o `cut`.

## 4. Edición de geometría

- **Agujeros** (`add_hole`): pasantes o ciegos, múltiples posiciones, en cualquier cara plana;
  tipos simple, avellanado (counterbore) y cónico (countersink).
- **Fillet** y **chamfer** (`fillet`, `chamfer`): todas las aristas o solo las seleccionadas.
- **Booleanas** (`boolean`): unión, sustracción, intersección.
- **Patrones:** lineal (`linear_pattern`) y circular (`circular_pattern`).
- **Espejo** (`mirror`) respecto a planos XY / XZ / YZ con offset.
- **Edición de caras:** extrusión/empuje-tirón (`face_extrude`), inset (`face_inset`),
  offset (`face_offset`).
- **Escala** uniforme o por eje (`scale`).
- **Eliminar** objetos (`delete_objects`) con simulación previa (`check_delete`).
- **Eliminar feature** con comprobación de dependencias (`remove_feature`).
- **Suprimir / reactivar** features (`suppress_feature`).

## 5. Transformaciones y organización de objetos

- **Mover** (`move_object`) y **rotar** (`rotate_object`), relativo o absoluto; `transform` combinado.
- **Mover / rotar interactivo** (tecla `G` / `R`) estilo Blender: restricción por eje o plano,
  pivote origen/centro, snapping, HUD, confirmar/cancelar, un solo paso de undo.
- **Dimensiones de objeto:** medir (`get_object_dimensions`), cambiar una dimensión
  (`set_object_dimension`), redimensionar (`resize_object`), **bloqueo de proporciones**
  (`set_object_proportional_scaling`) y ancla (centro / origen del objeto).
- **Duplicar** (`duplicate`): copias enlazadas con desplazamiento.
- **Alinear** (`align`): por mínimo, centro o máximo en uno o más ejes.
- **Grupos** (`group`, `ungroup`): organización en el árbol de diseño.
- **Consulta de transformación** (`get_object_transform`): origen, orientación, ejes locales.

## 6. Parámetros y paramétrica

- **Conjuntos de parámetros de diseño** (`define_parameters`, `set_parameter`).
- **Modificar parámetros** de una feature (`modify_parameter`), también inline desde el árbol.
- **Expresiones** (`set_expression`): números, `+ - * / ^`, paréntesis, funciones, referencias a
  parámetros de otras features.
- **Enlace de parámetros** (`bind_parameters`): varios parámetros guiados por expresiones en un
  solo ChangeSet.
- **Renombrar** features (`rename_feature`).
- **Resumen del modelo** (`get_model_summary`): features, parámetros, bounding boxes, volúmenes,
  checkpoints.

## 7. Componentes reutilizables

- **Crear componente** desde lo ya modelado (`create_component`).
- **Insertar instancias** (`insert_component`), también componentes dentro de componentes.
- **Sincronizar** instancias tras editar el componente (`sync_component`).
- **Separar** una instancia en copia local (`detach_component`).
- **Biblioteca:** listar (`list_components`), buscar (`search_components`), abrir (`open_component`).
- Publicar el proyecto actual como componente.

## 8. Apariencia y materiales

- **Color** (`set_color`), **opacidad** (`set_opacity`), **visibilidad** (`set_visibility`),
  consulta (`get_appearance`) y restablecer (`reset_appearance`). Solo presentación; no afecta
  geometría ni exportación.
- **Biblioteca de materiales** (~75 materiales integrados: aluminio cepillado/pulido/anodizado,
  aceros, latón, cobre, titanio, ABS, PLA, PETG, nylon, gomas, fibra de carbono, vidrio, madera, PCB,
  etc.) más materiales de proyecto.
- **Asignar / quitar material** (`set_material`, `remove_material`), escala de textura
  (`set_material_scale`), listar / buscar / leer (`list_materials`, `search_materials`,
  `get_material`).
- **Render PBR:** clearcoat, transmisión (vidrio), ior, sheen, emisivo y texturas CC0; mapeo
  triplanar.
- Panel de materiales en la UI.

## 9. Medición, unidades, grid y snapping

- **Medición** entre dos puntos / caras / cuerpos (`measure_distance`; tecla `M` en el viewport).
- **Unidades de visualización:** lineales mm / cm / m / in / ft; angulares grados / radianes;
  decimales (`set_units`, `convert_units`).
- **Grid de referencia** (`configure_grid`): visibilidad, plano (XY / XZ / YZ), espaciado, líneas
  mayores, extensión, origen.
- **Snapping** (`configure_snapping`, `snap_calculate`): grid, traslación, rotación e incrementos.
- **Configuración de workspace por proyecto** (`get_workspace_settings`,
  `restore_workspace_defaults`), con valores por defecto globales para proyectos nuevos.
- **Detalle de malla** (`set_mesh_detail`): coarse / normal / fine (solo visual).

## 10. Historial, checkpoints y replay

- **Undo / Redo** de ChangeSets.
- **Checkpoints semánticos** con título, autor y número de cambios.
- **Restaurar checkpoint** (`restore_checkpoint`), propuesto como ChangeSet (también deshacible).
- **Comparar** checkpoints (diff de features y parámetros, delta de volumen).
- **Replay** determinista del historial y verificación de que reproduce el modelo.
- **Instant Design playback:** reproduce los ChangeSets uno a uno en el viewport.
- **Línea de tiempo semántica** en la UI.

## 11. Agente de IA

- **Lenguaje natural → llamadas a herramientas** estructuradas con corrección guiada por errores
  accionables.
- **Contexto de selección:** el agente recibe descriptores (tipo de superficie, normal, centroide,
  bbox, área) de las entidades seleccionadas.
- **Flujo autónomo** (`--autonomous`): planificar → ejecutar → validar → autocorregir.
- **Niveles de interacción:** *Review* (previsualiza todo) y *Auto* (confirma cambios simples de
  bajo riesgo).
- **Política de commit:** auto / preview / commit forzado.
- **Proveedores:** Claude, OpenAI, Ollama (local).
- **Modelo de fallback** con política de consentimiento (`ask` / `allow` / `deny`) y enrutamiento
  automático (adjuntos o peticiones específicas al modelo de respaldo).
- **Enrutamiento local-first por niveles** (Local · Cloud Standard · Cloud Advanced): cada petición
  se clasifica sin coste de API (reglas y, si hace falta, el modelo local), se valida con el
  resultado real del motor CAD y escala solo si falla de forma recuperable y el modo
  (`local_first` / `local_only` / `cloud_preferred` / `manual`) y el consentimiento lo permiten.
  Cada petición deja una traza (niveles, intentos, escaladas, coste estimado, latencia) sin
  contenido del prompt.
- **Control de esfuerzo y de coste** (consumo de tokens, caché de prompts).
- **Caché de prompts de Anthropic** sobre las definiciones de herramientas y el prompt de sistema.
- **Herramientas categorizadas** (11 categorías: primitivas, sketch, edición, transformación,
  paramétrica, componentes, apariencia, workspace, proyecto, imagen, refinamiento) y **carga bajo
  demanda** (`SIMON_TOOL_LOADING=progressive`, por defecto): solo se envían las herramientas núcleo más la
  meta-herramienta `load_tool_category`; las categorías cargadas se acumulan sin romper la caché.
  En el chat de la UI es el modo por defecto; `SIMON_TOOL_LOADING=all` envía todas (evals y CLI usan `all`).
- **Grabación y reproducción** de conversaciones (recordings) para ejecutar sin API key.
- **Evals** grabados y en vivo.
- **Capacidades conectables:** `geometry`, `vision`, `visual`.

## 12. Imagen a CAD y documentos

- **Adjuntos** clasificados y enrutados por capacidad (imagen, PDF).
- **Interpretación de imagen** (`analyze_image`) → propuesta de sketch estructurada con informe
  de validación (`get_sketch_proposal`, `update_sketch_proposal`).
- **Calibración de escala y cámara** (`set_sketch_scale`, `calibrate_reference_image`).
- **Confirmación de dimensiones detectadas** (`confirm_detected_dimension`).
- **Crear geometría desde imagen:** solo sketch (`create_sketch_from_image`), extrusión de perfiles
  detectados (`extrude_detected_profile`) o modelo completo (`create_from_image`).
- **Documentos de especificación** como entrada del agente.
- Pausa del agente para revisión tras una interpretación de imagen.

## 13. Refinamiento visual

- **Bucle cerrado:** planificador, constructor CAD, crítico de visión y controlador.
- **Render offscreen** desde cámaras fijas (`render_model`): izquierda, derecha, frontal, trasera,
  superior, 3/4.
- **Estado visual** (`get_visual_state`): referencias, proporciones, regiones, parámetros.
- **Regiones** (`define_region`) y **bloqueos** (`lock_region`, `unlock_region`) para proteger
  geometría ya correcta.
- Comparación visual (siluetas, proporciones, puntuación por etapas).

## 14. Interfaz de usuario

- **Panel del agente** (Simon Agent) con selección mostrada como chips en el prompt.
- **Árbol de diseño:** visibilidad, icono por tipo, badge `agent`, selección múltiple, detalles
  (intención, errores, apariencia, parámetros), renombrar (F2), menú contextual, grupos plegables.
- **Barra de acciones contextual:** solo muestra las acciones válidas para la selección
  (cuerpo / cara / arista / vértice / varios); filtro de selección Auto / Body / Face / Edge / Vertex.
- **Paleta de comandos** (Ctrl/Cmd+K): verbos de intención, todas las herramientas y comandos.
- **Previsualización de ChangeSet:** fantasma translúcido, cuerpos reemplazados atenuados, lista de
  comandos, advertencias, validaciones y delta de volumen.
- **Edición inline** de dimensiones desde etiquetas del viewport y del panel de Selección.
- **Barra superior:** proyecto (Nuevo / Abrir), Undo, Redo, Guardar, Exportar, Ayuda, atajos, menú
  de desbordamiento.
- **Paneles** redimensionables y colapsables (tamaños persistentes); overlays en pantallas estrechas.
- **Soporte táctil:** controles de 40–44 px, pulsación larga = menú contextual.
- **Ajustes** con navegación por secciones: Modelo primario, Modelo de fallback, Routing, Configuración de
  UI, Viewport, Rendering, Workspace / Units & Grid.
- **Modo Print** (barra superior / paleta): simulación de impresión 3D, ver [§22](#22-simulación-de-impresión-3d).
- **Concurrencia:** el turno del agente no bloquea la lectura de estado; las ediciones se rechazan
  mientras trabaja.

## 15. Viewport y renderizado

- Three.js (r186, incluido en `frontend/vendor/`), sin paso de build.
- **Navegación:** orbitar, pan, zoom, ajustar vista (Fit), cubo de vistas.
- **Single / Quad View:** 1 vista o 4 (Top | Perspective / Front | Right), cámaras perspectiva y
  ortográfica por panel, siete orientaciones, divisores redimensionables, maximizar un panel.
- **Selección:** clic, Shift-clic (añadir), Ctrl-clic (alternar), selección por caja (`B`), resaltado
  al pasar, filtro de tipo, picking de caras / aristas / vértices.
- **Plano de sección** (X / Y / Z + offset).
- **Dimensiones exteriores** del objeto seleccionado, editables.
- **Ejes de coordenadas** (X rojo, Y verde, Z azul) y gizmo de ejes configurable.
- **Iluminación de estudio** procedural, sombras suaves, entorno (estudio o sala), exposición,
  tone mapping (neutral, ACES, AgX, ninguno), calidad de sombras (off / standard / high).
- Solo presentación: nada de esto entra en el proyecto, el historial ni las exportaciones.
- Layout de vistas guardado por proyecto en `localStorage`.

## 16. Atajos de teclado y mouse

| Grupo | Atajos |
|---|---|
| Esenciales | Ctrl+Z deshacer · Ctrl+Shift+Z / Ctrl+Y rehacer · Ctrl+S guardar · Supr/Backspace eliminar · Esc cerrar/limpiar · Ctrl+K paleta · `?` atajos |
| Selección y edición | Clic · Shift+clic · Ctrl+clic · `B` caja · `G` mover · `R` rotar · Ctrl+D duplicar · Ctrl+G agrupar · Ctrl+Shift+G desagrupar · clic derecho menú |
| Vista y cámara | `F` fit · rueda zoom · arrastre orbitar/pan · arrastre central/derecho pan · `M` medir · Ctrl+Alt+4 Single/Quad · doble clic maximizar |
| Durante mover/rotar | Clic/Enter confirmar · Esc/clic derecho cancelar · `X`/`Y`/`Z` eje · Shift+`X`/`Y`/`Z` plano · Ctrl sin snapping · `P` pivote |
| Simulación de impresión | Espacio reproducir/pausar · `[` capa anterior · `]` capa siguiente |
| Simon Agent | Enter enviar · Shift+Enter nueva línea |
| Árbol y menús | ↑ ↓ navegar · Enter · F2 renombrar · ← → plegar grupo |

Panel de atajos: botón de teclado, *More → Keyboard shortcuts* o `?`.

## 17. CLI

`simoncad-cli` (todos los comandos aceptan `--json`):

| Comando | Función |
|---|---|
| `create` | Proyecto nuevo desde lenguaje natural |
| `modify` | Modificar un proyecto desde lenguaje natural (con `--select`) |
| `tool` | Invocar una herramienta de forma determinista |
| `tools` | Listar herramientas y esquemas |
| `build` | Reconstruir y reportar estado por feature y caché |
| `export` | Exportar STEP / STL con verificaciones |
| `history` | Checkpoints, ChangeSets, aprobaciones pendientes, diff |
| `restore` | Volver a un checkpoint |
| `commit` / `reject` | Aprobar o descartar un ChangeSet propuesto |
| `undo` | Deshacer el último ChangeSet |
| `replay` | Re-derivar el modelo y verificarlo |
| `eval` | Ejecutar evals del agente (grabados o `--live`) |
| `bracket` | Prueba de concepto del soporte de servo (Fase 0) |

Banderas del agente: `--preview`, `--commit`, `--label`, `--autonomous`.

`simoncad` (UI): `make ui`, `--project`, `--no-browser`, `--recording`.

## 18. Proyectos, persistencia y exportación

- **Formato `.scadproj`** (ver [project-format.md](project-format.md)).
- **Exportación STEP** con verificación de reimportación.
- **Exportación STL** binario con comprobación de estanqueidad (watertight) para impresión 3D.
- Proyectos nuevos / abrir / guardar desde la UI.
- Bibliotecas de componentes y materiales de proyecto.

## 19. Configuración y proveedores de modelo

- Variables en `.env`: `SIMONCAD_PROVIDER`, claves y modelos de Claude / OpenAI / Ollama,
  `OLLAMA_NUM_CTX`, `OLLAMA_VISION`, `SIMONCAD_AUTONOMOUS`, `SIMONCAD_ROUTING_MODE`, `SIMONCAD_LOCAL_RETRIES`, `SIMONCAD_STANDARD_RETRIES`, `SIMONCAD_ADVANCED_*`, `SIMON_TOOL_LOADING`, `SIMONCAD_PRUSASLICER`, fallback
  (`SIMONCAD_FALLBACK_*`, `SIMONCAD_FALLBACK_CONSENT`).
- Ajustes de UI: modelos, viewport, rendering y workspace, con previsualización en vivo y Cancel
  que revierte.

## 20. Calidad, seguridad y pruebas

- **Pruebas:** C++ del puente, `go vet`, `go test`, evals grabados, pruebas unitarias del frontend
  (`*.test.mjs`) y E2E con Chrome (`make e2e`).
- **Garantías verificadas:** la UI no tiene ruta de mutación fuera del registro de herramientas;
  biyección pick ID ↔ selector; solo se re-mallan y reenvían cuerpos cambiados; la vista previa
  nunca toca el modelo vivo.
- **Seguridad:** servidor local en loopback con token por ejecución y verificación de Host.
- `make leakcheck` (valgrind) sobre el ABI.

## 21. Ayuda integrada

- **Microsite de ayuda** (`frontend/help.html`): guía de usuario con prompts de ejemplo por
  función, sección de simulación de impresión, ejemplos de extremo a extremo (soporte de servo, separadores paramétricos, brida con
  círculo de pernos, carcasa), referencia de herramientas, atajos, configuración (incluye `SIMONCAD_PRUSASLICER` y
  `SIMON_TOOL_LOADING`) y solución de
  problemas.

## 22. Simulación de impresión 3D

- Modo **Print** (barra superior o paleta): rebanado en segundo plano con PrusaSlicer (subproceso, instalado
  por el usuario) o un slicer *preview* interno, y reproducción de los toolpaths reales en el mismo viewport:
  cama con marcas en mm, nozzle animado, filamento depositado capa por capa.
- Línea de tiempo con velocidades 1x–500x, salto y búsqueda sin reproducir, inspección de capa, filtros
  (viajes, paredes, relleno, soportes), overlay translúcido del modelo original y tres presets de
  visualización (Realista, Técnico, Diagnóstico).
- Perfil genérico etiquetado como *simulación, no hardware verificado*; resultados cacheados por revisión
  de geometría y ajustes; cancelación; el modelo CAD nunca se modifica. Detalle en `docs/printsim.md`.
- Atajos: `Espacio` reproducir/pausar, `[` / `]` capa anterior/siguiente.
- Slicer `auto` por defecto: PrusaSlicer si está disponible (`SIMONCAD_PRUSASLICER`), si no el preview interno
  (sin soportes ni puentes).
- Pendiente de esta línea: análisis de imprimibilidad, herramientas del agente/Critic, modo cinemático.

## 23. Paths, sweeps, vectorización, relieves, sellos y texto

- **Paths 3D** (`create_path`, `update_path`, `evaluate_path`): lineales, Bézier, Catmull-Rom y B-spline, con ancho, alto y
  twist por punto; longitud de arco, remuestreo, simplificación y suavizado. Editor interactivo en el viewport
  (puntos, handles, inserción, cierre) con preview en vivo.
- **Perfiles y sweeps** (`create_profile`, `create_sweep`, `create_tube`, `create_wire`, `create_ribbon`): sólidos B-Rep
  exactos (OCCT) con marcos de transporte paralelo, tapas planas/redondas/punta, tubos huecos y paths cerrados.
  Errores con código (`PATH_SELF_INTERSECTION`, `EXCESSIVE_CURVATURE`, `SWEEP_FAILED`, …) y pista de corrección.
- **Vectorización** (`vectorize_image`, `image_to_paths`): PNG/JPEG/SVG a regiones rellenas con agujeros, contornos Bézier
  o líneas centrales con ancho de trazo. Determinista; la escala real es obligatoria (`SCALE_REQUIRED`).
- **Relieves** (`create_relief`, `engrave_surface`): emboss/deboss/engrave de sketches, texto o paths sobre un cuerpo,
  todo editable (`RELIEF_DISCONNECTED`, `RELIEF_NO_EFFECT`).
- **Sellos** (`create_stamp`, `apply_recipe`, `update_recipe`, `stamp_impression`, `validate_printability`): placa, diseño
  en relieve espejado, mango, bordes redondeados y compensación de contracción (nunca se asume: la da el usuario).
- **Texto 3D** (`create_text_geometry`): tipografía TrueType/OpenType (fuente integrada Go Regular o un `.ttf/.otf`),
  kerning, espaciado, alineación, saltos de línea y espejo; los contadores de la "o", "A", "B" se conservan.
  Modos: sketch, extrude, emboss, engrave; el texto puede seguir un path. `update_text` cambia palabras, tamaño, fuente o ubicación y regenera todo lo construido encima. Un sketch de texto sirve como diseño de un
  sello (`create_stamp` con `sketch`).
- **Proyección sobre superficies** (`project_path_to_surface`): proyecta un path sobre un cuerpo (BVH sobre su teselado) en modo
  `planar` (según una dirección), `normal` (punto más cercano) o `conform` (puntos extra donde la cuerda se separa de la
  superficie más que la tolerancia), con `offset` a lo largo de la normal. Crea un path nuevo (instantánea: se vuelve a
  ejecutar con `refresh_projection`, que reaplica los mismos ajustes y regenera lo que depende del path cuando el cuerpo cambia); alimenta `engrave_surface` y los sweeps. Los puntos que no alcanzan la superficie se
  informan por índice (`SURFACE_PROJECTION_FAILED`). El texto sobre piezas curvas se hace con letras de tope plano que
  nacen dentro de la superficie (offset de plano bajo la superficie); no se deforma cada letra sobre la curvatura.
- **Dibujo a sólido** (`sketch_to_solid`, `check_solid_vs_trace`): un dibujo o silueta (PNG/JPEG/SVG) pasa a sólido extruido en un
  paso (traza → sketch → extrude, agrupado y editable). El tamaño real y la altura son obligatorios (nada se inventa), el estilo
  `strokes` engrosa el trazo de un dibujo de líneas, y el cambio se propone para aprobación del usuario con la lista de
  supuestos. El crítico determinista compara el sólido con lo que prometía el trazo (volumen = área × altura, huella,
  cuerpo único); la comparación visual multivista con la imagen original usa `render_model` y el bucle de refinamiento existente.
- Detalle de las herramientas en `docs/tools.md`.

## 24. Fuera de alcance / pendiente

- Fases 3–7: catálogo y macros de robótica, análisis, diseño generativo, ensamblajes, juntas y
  cinemática.
- Loft, shell, restricciones avanzadas de sketch.
- Agujeros desde caras no alineadas a ejes.
- Selector semántico `kind=external` para aristas.
- Shell de escritorio Wails (hoy servidor loopback + navegador).
- Streaming de texto del modelo; llaves de API en keychain del SO.
- Resultados en vivo del LLM aún sin medir.
