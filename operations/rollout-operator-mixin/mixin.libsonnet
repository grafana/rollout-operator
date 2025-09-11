(import 'config.libsonnet') + {
  _config+: {
    product: 'Mimir',
  },
} +
(import 'dashboards.libsonnet')
