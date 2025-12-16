{
  // The alert name is prefixed with the product name (eg. AlertName -> MimirAlertName).
  alertName(name)::
    $._config.rollout_operator_alert_name + name,

  withRunbookURL(url_format, groups)::
    local update_rule(rule) =
      if std.objectHas(rule, 'alert')
      then rule {
        annotations+: {
          runbook_url: url_format % std.asciiLower(rule.alert),
        },
      }
      else rule;
    [
      group {
        rules: [
          update_rule(alert)
          for alert in group.rules
        ],
      }
      for group in groups
    ],

  withExtraLabelsAnnotations(groups)::
    local update_rule(rule) =
      if std.objectHas(rule, 'alert')
      then rule {
        annotations+: $._config.alert_extra_annotations,
        labels+: $._config.alert_extra_labels,
      }
      else rule;
    [
      group {
        rules: [
          update_rule(rule)
          for rule in group.rules
        ],
      }
      for group in groups
    ],
}
