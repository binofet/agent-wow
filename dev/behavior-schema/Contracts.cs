using Metagen;
namespace WarcraftBuddy.Behavior;

[MetaSchema("warcraftbuddy.behavior.combatant", 1, "Combatant")]
public sealed class Combatant {
    [MetaField(1, "guid")] public string Guid { get; set; } = "";
    [MetaField(2, "health")] public uint Health { get; set; }
    [MetaField(3, "maxHealth")] public uint MaxHealth { get; set; }
    [MetaField(4, "classId")] public uint ClassId { get; set; }
    [MetaField(5, "power")] public uint Power { get; set; }
    [MetaField(6, "maxPower")] public uint MaxPower { get; set; }
}

[MetaSchema("warcraftbuddy.behavior.spell", 1, "Spell")]
public sealed class Spell {
    [MetaField(1, "spellId")] public uint SpellId { get; set; }
    [MetaField(2, "cost")] public uint Cost { get; set; }
    [MetaField(3, "amount")] public double Amount { get; set; }
    [MetaField(4, "castMs")] public uint CastMs { get; set; }
    [MetaField(5, "score")] public double Score { get; set; }
}

[MetaSchema("warcraftbuddy.behavior.input", 1, "Input")]
public sealed class Input {
    [MetaField(1, "kind")] public string Kind { get; set; } = "";
    [MetaField(2, "self")] public Combatant Self { get; set; } = new();
    [MetaField(3, "target")] public Combatant Target { get; set; } = new();
    [MetaField(4, "spells")] public List<Spell> Spells { get; set; } = [];
    [MetaField(5, "targets")] public List<Combatant> Targets { get; set; } = [];
    [MetaField(6, "defaultTargetGuid")] public string DefaultTargetGuid { get; set; } = "";
}

[MetaSchema("warcraftbuddy.behavior.decision", 1, "Decision")]
public sealed class Decision {
    [MetaField(1, "spellId")] public uint SpellId { get; set; }
    [MetaField(2, "targetGuid")] public string TargetGuid { get; set; } = "";
}
