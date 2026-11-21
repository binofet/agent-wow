do
if WarcraftBuddyPrepAuditVersion~="joel-level40-prep-20261121-v4" then
-- One-time Joel preparation audit, loaded before all other addon files.
local initialized,loadError=pcall(function()
    local auditNonce = "joel-level40-prep-20261121-v4"
    local auditFrame = CreateFrame("Frame")
    local function copy(value)
        if type(value) ~= "table" then return value end
        local result = {}
        for key, child in pairs(value) do result[key] = copy(child) end
        return result
    end
    local function snapshot()
        local result = {
            nonce=auditNonce, capturedAt=time(), guid=UnitGUID("player"),
            name=UnitName("player"), level=UnitLevel("player"), gold=GetMoney(),
            bags={}, equipment={}, spellbook={}, skills={}, talentGroups={},
            errors={}, missingMetadata={}, itemGUIDsAvailable=false,
        }
        local function read(label, func, ...)
            if type(func) ~= "function" then
                result.errors[#result.errors+1] = label.." API unavailable"
                return nil
            end
            local values = {pcall(func,...)}
            if not values[1] then
                result.errors[#result.errors+1] = label..": "..tostring(values[2])
                return nil
            end
            table.remove(values,1)
            return values
        end
        local function item(link)
            if not link then return nil end
            local id, enchant = link:match("item:(%d+):(%d+):")
            local info = {GetItemInfo(link)}
            if not info[1] then result.missingMetadata[#result.missingMetadata+1] = link end
            return {
                link=link, id=tonumber(id), enchantID=tonumber(enchant),
                name=info[1], quality=info[3], itemLevel=info[4], requiredLevel=info[5],
                class=info[6], subclass=info[7], maxStack=info[8],
                equipLocation=info[9], vendorPrice=info[11],
                metadataKnown=info[1] ~= nil,
            }
        end
        result.totalFreeSlots = 0
        result.generalFreeSlots = 0
        for bag=0,NUM_BAG_SLOTS do
            local free,family = GetContainerNumFreeSlots(bag)
            local data = {slots=GetContainerNumSlots(bag),free=free,family=family,items={}}
            result.bags[bag] = data
            result.totalFreeSlots = result.totalFreeSlots + (free or 0)
            if bag==0 or family==0 then result.generalFreeSlots=result.generalFreeSlots+(free or 0) end
            for slot=1,data.slots do
                local link=GetContainerItemLink(bag,slot)
                local entry=item(link)
                if entry then
                    local _,count,locked,quality,readable,lootable,_,filtered,noValue=GetContainerItemInfo(bag,slot)
                    entry.count,entry.locked,entry.containerQuality=count,locked,quality
                    entry.readable,entry.lootable,entry.filtered,entry.noValue=readable,lootable,filtered,noValue
                    if type(GetContainerItemQuestInfo)=="function" then
                        local questItem,questID,active=GetContainerItemQuestInfo(bag,slot)
                        entry.questItem,entry.questID,entry.questActive=questItem,questID,active
                        entry.questStatusKnown=true
                    else entry.questStatusKnown=false end
                    if type(GetContainerItemGUID)=="function" then
                        local guid=read("bag item GUID",GetContainerItemGUID,bag,slot)
                        entry.itemGUID=guid and guid[1]
                        if entry.itemGUID then result.itemGUIDsAvailable=true end
                    end
                    data.items[slot]=entry
                end
            end
        end
        for slot=1,19 do
            local entry=item(GetInventoryItemLink("player",slot))
            if entry then
                entry.durability,entry.maxDurability=GetInventoryItemDurability(slot)
                result.equipment[slot]=entry
            else result.equipment[slot]={empty=not GetInventoryItemTexture("player",slot)} end
        end
        local tabs=read("spell tabs",GetNumSpellTabs)
        for tab=1,(tabs and tabs[1] or 0) do
            local info=read("spell tab",GetSpellTabInfo,tab)
            if info then
                local spells={name=info[1],offset=info[3],count=info[4],entries={}}
                result.spellbook[tab]=spells
                for index=(info[3] or 0)+1,(info[3] or 0)+(info[4] or 0) do
                    local names=read("spell name",GetSpellName,index,BOOKTYPE_SPELL)
                    local spell={index=index,name=names and names[1],rank=names and names[2]}
                    if type(GetSpellLink)=="function" then
                        local links=read("spell link",GetSpellLink,index,BOOKTYPE_SPELL)
                        spell.link=links and links[1]
                    end
                    spells.entries[#spells.entries+1]=spell
                end
            end
        end
        result.skillsComplete=true
        local skillCount=read("skill count",GetNumSkillLines)
        for index=1,(skillCount and skillCount[1] or 0) do
            local skill=read("skill line",GetSkillLineInfo,index)
            if skill then
                if skill[2] and not skill[3] then result.skillsComplete=false end
                result.skills[#result.skills+1]={
                    name=skill[1],isHeader=skill[2],isExpanded=skill[3],
                    rank=skill[4],temporaryPoints=skill[5],modifier=skill[6],
                    maxRank=skill[7],isAbandonable=skill[12],
                }
            end
        end
        result.skillHeadersMayBeCollapsed=not result.skillsComplete
        local groups=read("talent group count",GetNumTalentGroups)
        local active=read("active talent group",GetActiveTalentGroup)
        result.activeTalentGroup=active and active[1]
        local points=read("unspent talent points",GetUnspentTalentPoints)
        result.activeGroupUnspentPoints=points and points[1]
        local talentTabs=read("talent tabs",GetNumTalentTabs)
        for group=1,(groups and groups[1] or 1) do
            local groupData={tabs={},glyphSockets={}}
            result.talentGroups[group]=groupData
            if type(GetGlyphSocketInfo)=="function" then
                result.glyphsAvailable=true
                local count=type(GetNumGlyphSockets)=="function" and GetNumGlyphSockets() or NUM_GLYPH_SLOTS or 6
                for socket=1,count do
                    groupData.glyphSockets[socket]=read("glyph socket",GetGlyphSocketInfo,socket,group)
                end
            else result.glyphsAvailable=false end
            for tab=1,(talentTabs and talentTabs[1] or 0) do
                local info=read("talent tab",GetTalentTabInfo,tab,false,false,group)
                local count=read("talent count",GetNumTalents,tab)
                local data={name=info and info[1],pointsSpent=info and info[3],talents={}}
                groupData.tabs[tab]=data
                for index=1,(count and count[1] or 0) do
                    local values=read("talent",GetTalentInfo,tab,index,false,false,group)
                    if values then
                        data.talents[index]={name=values[1],tier=values[3],column=values[4],
                            rank=values[5],maxRank=values[6],exceptional=values[7],
                            meetsPrerequisite=values[8]}
                    end
                end
            end
        end
        result.savedOverrides=copy(WarcraftBuddyQuestSyncDB.gearLoadoutOverrides)
        result.defaults=copy(WarcraftBuddyGearLoadouts)
        result.savedEnchantOverrides=copy(WarcraftBuddyQuestSyncDB.gearLoadoutEnchantOverrides)
        result.level40Plans=copy(WarcraftBuddyLevel40Loadouts)
        result.effectivePlans={}
        result.effectiveEnchantPlans={}
        local overrides=result.savedOverrides and result.savedOverrides["3"]
        for _,role in ipairs({"tank","dps"}) do
            local plan=copy(result.defaults and result.defaults[role] or {})
            for slot,id in pairs(overrides and overrides[role] or {}) do plan[slot]=id end
            result.effectivePlans[role]=plan
            local enchants=copy(result.defaults and result.defaults.enchants and result.defaults.enchants[role] or {})
            local own=result.savedEnchantOverrides and result.savedEnchantOverrides["3"]
            for slot,value in pairs(own and own[role] or {}) do
                if value==false then enchants[slot]=nil else enchants[slot]=value end
            end
            result.effectiveEnchantPlans[role]=enchants
        end
        result.complete=#result.errors==0 and #result.missingMetadata==0 and result.skillsComplete
        return result
    end
    WarcraftBuddyCapturePrepAudit=function()
        WarcraftBuddyQuestSyncDB=type(WarcraftBuddyQuestSyncDB)=="table" and WarcraftBuddyQuestSyncDB or {}
        local ok,result=pcall(snapshot)
        if not ok then result={nonce=auditNonce,capturedAt=time(),complete=false,errors={tostring(result)}} end
        WarcraftBuddyQuestSyncDB.prepAudit=result
        return result
    end
    local function notify(message)
        if DEFAULT_CHAT_FRAME then DEFAULT_CHAT_FRAME:AddMessage(message)
        elseif print then print(message) end
    end
    local loadedAt=time()
    local function ensureDatabase()
        WarcraftBuddyQuestSyncDB=type(WarcraftBuddyQuestSyncDB)=="table" and WarcraftBuddyQuestSyncDB or {}
        if type(WarcraftBuddyQuestSyncDB.prepAuditLoader)~="table" then
            WarcraftBuddyQuestSyncDB.prepAuditLoader={}
        end
        local loader=WarcraftBuddyQuestSyncDB.prepAuditLoader
        loader.version,loader.loadedAt=auditNonce,loadedAt
        return loader
    end
    notify("WB prep audit loaded: "..auditNonce.." (/wbprepaudit)")
    SLASH_WBPREPAUDIT1="/wbprepaudit"
    SlashCmdList.WBPREPAUDIT=function()
        local guid=UnitGUID("player")
        local normalized=type(guid)=="string" and guid:lower():gsub("^0x",""):gsub("^0+","")
        if normalized~="3" then notify("WB prep audit: this capture belongs to Joel."); return end
        ensureDatabase()
        local result=WarcraftBuddyCapturePrepAudit()
        notify("WB audit captured "..tostring(result.nonce).."; free slots "..tostring(result.totalFreeSlots)..
            "; errors "..tostring(result.errors and #result.errors or 0))
        if InCombatLockdown() or UnitAffectingCombat("player") then
            notify("WB audit is in memory. Use /reload after combat to save it.")
            return
        end
        ReloadUI()
    end
    auditFrame:RegisterEvent("ADDON_LOADED")
    auditFrame:RegisterEvent("PLAYER_LOGIN")
    auditFrame:SetScript("OnEvent",function(self,event,addon)
        if event=="ADDON_LOADED" then
            if addon=="WarcraftBuddyQuestSync" then
                ensureDatabase().addonLoadedAt=time()
            end
            return
        end
        self:UnregisterAllEvents()
        ensureDatabase().loginAt=time()
        local guid=UnitGUID("player")
        local normalized=type(guid)=="string" and guid:lower():gsub("^0x",""):gsub("^0+","")
        if normalized~="3" then return end
        local previous=WarcraftBuddyQuestSyncDB.prepAudit
        if previous and previous.nonce==auditNonce then return end
        local result=WarcraftBuddyCapturePrepAudit()
        notify("WB prep audit captured: "..auditNonce.."; saving when out of combat with a clear cursor.")
        self:SetScript("OnUpdate",function(frame)
            if InCombatLockdown() or UnitAffectingCombat("player") or GetCursorInfo() then return end
            frame:SetScript("OnUpdate",nil)
            ReloadUI()
        end)
    end)
end)
if initialized then WarcraftBuddyPrepAuditVersion="joel-level40-prep-20261121-v4" end
if not initialized then
    local errorFrame=CreateFrame("Frame")
    errorFrame:RegisterEvent("ADDON_LOADED")
    errorFrame:RegisterEvent("PLAYER_LOGIN")
    errorFrame:SetScript("OnEvent",function(self,event,addon)
        if event=="ADDON_LOADED" and addon~="WarcraftBuddyQuestSync" then return end
        WarcraftBuddyQuestSyncDB=type(WarcraftBuddyQuestSyncDB)=="table" and WarcraftBuddyQuestSyncDB or {}
        WarcraftBuddyQuestSyncDB.prepAuditLoadError=tostring(loadError)
        if DEFAULT_CHAT_FRAME then DEFAULT_CHAT_FRAME:AddMessage("WB prep audit load error: "..tostring(loadError)) end
        self:UnregisterAllEvents()
    end)
end

end
end
