# Guía de NeoProxy

NeoProxy es el gateway de inferencia: recibe una solicitud con un modelo y la encamina a un proveedor configurado. No ejecuta por sí solo las tareas del agente ni reemplaza la revisión humana. El modelo genera respuestas; el motor del agente las combina con instrucciones y herramientas; la interfaz es el lugar donde una persona trabaja con él.

## Dónde encaja

En el diseño de oficina se distinguen tres servicios: NeoProxy para inferencia, NeoDeck como cockpit web del equipo y NeoCore como futuro daemon de agentes. NeoCore todavía no está implementado y no hay que suponer que los tres ya están desplegados o integrados. Los dos clientes instalables son NeoPi (motor más interfaz de terminal) y NeoCode (interfaz gráfica). NeoPi conserva la ejecución local, sin exigir un daemon separado. En el futuro se busca conectar las interfaces a agentes alojados localmente o en un daemon local o remoto; no es una capacidad que esta guía dé por disponible hoy. Véase el [panorama general](https://github.com/CooperbitQuarxs/neopi/blob/neopi/docs/panorama-general.md).

## Qué existe y qué falta

En la rama actual, el proxy admite claves para autenticar clientes y configuración de proveedores y alias de modelos. El [ejemplo de configuración](../config.example.yaml) distingue las claves de acceso del cliente de las credenciales hacia proveedores y muestra correspondencias entre nombre y alias. Esas funciones no equivalen, por sí solas, a una puesta en marcha de oficina ni garantizan que cada persona tenga sus modelos autorizados.

El objetivo de oficina es que la responsable configure cuentas y claves individuales, y administre centralmente roles expuestos como modelos virtuales: por ejemplo, una etiqueta de trabajo podría apuntar a un modelo y nivel de esfuerzo que se actualicen sin pedirle a cada persona que cambie su elección. La posibilidad de elegir explícitamente un modelo seguirá donde esté disponible y autorizada. La política concreta, los accesos por persona y el despliegue aún deben configurarse; no se presenta esa operación como terminada ni se presupone que el mapeo de rol y esfuerzo esté disponible en esta rama.

## Primer acercamiento

1. Pedí a la responsable el acceso autorizado y el procedimiento vigente para tu cliente. No publiques claves ni compartas credenciales de cuentas personales; el uso debe respetar las condiciones de cada proveedor.
2. En NeoPi o NeoCode, una vez configurado el acceso, elegí un modelo disponible. Si se ofrecen roles, elegí el apropiado para la tarea; la disponibilidad depende de la configuración efectiva.
3. Describí el resultado deseado, el contexto permitido y cómo comprobarlo. Revisá la respuesta y la evidencia antes de aplicarla; iterá con correcciones. El conocimiento del dominio sigue siendo indispensable.
4. Ante un problema, registrá el mensaje y el momento sin incluir claves ni datos sensibles y consultá a la responsable. Un modelo no disponible puede depender tanto del proveedor como de la configuración de acceso.

## Para quien administra o integra

Separá la autenticación del cliente de las credenciales de proveedores. Antes de exponer un servicio, definí autorización, transporte seguro, manejo y rotación de secretos y condiciones de uso. No trates una clave de inferencia como autorización general para controlar trabajo de otras personas. La intención de acceso remoto es HTTPS o WebSocket seguro; el protocolo de autenticación entre servicios no está aprobado todavía.

- [Configuración de ejemplo](../config.example.yaml) y [API de administración v8](management-api-v8.md).
- [Guías de CLIProxyAPI](https://help.router-for.me/) y [API de administración upstream](https://help.router-for.me/management/api).
- [Repositorio de desarrollo del fork](https://github.com/PsychedelicShayna/NeoProxy) y [CLIProxyAPI upstream](https://github.com/router-for-me/CLIProxyAPI).

El código conserva su [licencia MIT](../LICENSE) y avisos de autoría.
