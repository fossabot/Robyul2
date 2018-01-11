package eventlog

import (
	"strings"

	"time"

	"strconv"

	"github.com/Seklfreak/Robyul2/cache"
	"github.com/Seklfreak/Robyul2/helpers"
	"github.com/Seklfreak/Robyul2/models"
	"github.com/bwmarrin/discordgo"
)

type Handler struct {
}

type action func(args []string, in *discordgo.Message, out **discordgo.MessageSend) (next action)

func (h *Handler) Commands() []string {
	return []string{
		"toggle-eventlog",
		"eventlog",
	}
}

func (h *Handler) Init(session *discordgo.Session) {
	defer helpers.Recover()

	session.AddHandler(h.OnChannelCreate)
	session.AddHandler(h.OnChannelDelete)
}

func (h *Handler) Uninit(session *discordgo.Session) {
	defer helpers.Recover()
}

func (h *Handler) Action(command string, content string, msg *discordgo.Message, session *discordgo.Session) {
	defer helpers.Recover()

	if !helpers.ModuleIsAllowed(msg.ChannelID, msg.ID, msg.Author.ID, helpers.ModulePermEventlog) {
		return
	}

	var result *discordgo.MessageSend
	args := strings.Fields(content)

	action := h.actionStart
	if command == "toggle-eventlog" {
		action = h.actionToggleEventlog
	}
	for action != nil {
		action = action(args, msg, &result)
	}
}

func (h *Handler) actionStart(args []string, in *discordgo.Message, out **discordgo.MessageSend) action {
	cache.GetSession().ChannelTyping(in.ChannelID)

	if len(args) < 1 {
		*out = h.newMsg("bot.arguments.too-few")
		return h.actionFinish
	}

	switch args[0] {
	//case "foo
	//	return h.actionFoo
	}

	*out = h.newMsg("bot.arguments.invalid")
	return nil
}

// [p]toggle-eventlog
func (h *Handler) actionToggleEventlog(args []string, in *discordgo.Message, out **discordgo.MessageSend) action {
	cache.GetSession().ChannelTyping(in.ChannelID)
	if !helpers.IsAdmin(in) {
		*out = h.newMsg("admin.no_permission")
		return h.actionFinish
	}

	channel, err := helpers.GetChannel(in.ChannelID)
	helpers.Relax(err)

	settings := helpers.GuildSettingsGetCached(channel.GuildID)
	var setMessage string
	if settings.EventlogDisabled {
		settings.EventlogDisabled = false
		setMessage = "plugins.eventlog.enabled"
	} else {
		settings.EventlogDisabled = true
		setMessage = "plugins.eventlog.disabled"
	}
	err = helpers.GuildSettingsSet(channel.GuildID, settings)
	helpers.Relax(err)

	*out = h.newMsg(setMessage)
	return h.actionFinish
}

func (h *Handler) actionFinish(args []string, in *discordgo.Message, out **discordgo.MessageSend) action {
	_, err := helpers.SendComplex(in.ChannelID, *out)
	helpers.RelaxMessage(err, in.ChannelID, in.ID)

	return nil
}

func (h *Handler) newMsg(content string, replacements ...interface{}) *discordgo.MessageSend {
	if len(replacements) < 1 {
		return &discordgo.MessageSend{Content: helpers.GetText(content)}
	}
	return &discordgo.MessageSend{Content: helpers.GetTextF(content, replacements...)}
}

func (h *Handler) OnMessage(content string, msg *discordgo.Message, session *discordgo.Session) {

}

func (h *Handler) OnMessageDelete(msg *discordgo.MessageDelete, session *discordgo.Session) {

}

func (h *Handler) OnGuildMemberAdd(member *discordgo.Member, session *discordgo.Session) {
	// handled in mod.go (to get invite code)
}

func (h *Handler) OnGuildMemberRemove(member *discordgo.Member, session *discordgo.Session) {
	go func() {
		defer helpers.Recover()

		leftAt := time.Now()

		helpers.EventlogLog(leftAt, member.GuildID, member.User.ID, models.EventlogTargetTypeUser, "", models.EventlogTypeMemberLeave, "", nil, nil)
	}()
}

func (h *Handler) OnReactionAdd(reaction *discordgo.MessageReactionAdd, session *discordgo.Session) {

}

func (h *Handler) OnReactionRemove(reaction *discordgo.MessageReactionRemove, session *discordgo.Session) {

}

func (h *Handler) OnGuildBanAdd(user *discordgo.GuildBanAdd, session *discordgo.Session) {

}

func (h *Handler) OnGuildBanRemove(user *discordgo.GuildBanRemove, session *discordgo.Session) {

}

func (h *Handler) OnChannelCreate(session *discordgo.Session, channel *discordgo.ChannelCreate) {
	go func() {
		// TODO: backfill, who created the channel
		defer helpers.Recover()

		leftAt := time.Now()

		options := make([]models.ElasticEventlogOption, 0)
		options = append(options, models.ElasticEventlogOption{
			Key:   "channel_name",
			Value: channel.Name,
		})

		switch channel.Type {
		case discordgo.ChannelTypeGuildCategory:
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_type",
				Value: "category",
			})
			break
		case discordgo.ChannelTypeGuildText:
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_type",
				Value: "text",
			})
			break
		case discordgo.ChannelTypeGuildVoice:
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_type",
				Value: "voice",
			})
			break
		}

		options = append(options, models.ElasticEventlogOption{
			Key:   "channel_topic",
			Value: channel.Topic,
		})

		if channel.NSFW {
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_nsfw",
				Value: "yes",
			})
		} else {
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_nsfw",
				Value: "no",
			})
		}

		if channel.Bitrate > 0 {
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_bitrate",
				Value: strconv.Itoa(channel.Bitrate),
			})
		}

		if channel.Position > 0 {
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_position",
				Value: strconv.Itoa(channel.Position),
			})
		}

		options = append(options, models.ElasticEventlogOption{
			Key:   "channel_parentid",
			Value: channel.ParentID,
		})

		/*
			TODO: handle permission overwrites
			options = append(options, models.ElasticEventlogOption{
				Key:   "permission_overwrites",
				Value: channel.PermissionOverwrites,
			})
		*/

		helpers.EventlogLog(leftAt, channel.GuildID, channel.ID, models.EventlogTargetTypeChannel, "", models.EventlogTypeChannelCreate, "", nil, options)
	}()
}

func (h *Handler) OnChannelDelete(session *discordgo.Session, channel *discordgo.ChannelDelete) {
	go func() {
		// TODO: backfill, who deleted the channel
		defer helpers.Recover()

		leftAt := time.Now()

		options := make([]models.ElasticEventlogOption, 0)
		options = append(options, models.ElasticEventlogOption{
			Key:   "channel_name",
			Value: channel.Name,
		})

		switch channel.Type {
		case discordgo.ChannelTypeGuildCategory:
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_type",
				Value: "category",
			})
			break
		case discordgo.ChannelTypeGuildText:
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_type",
				Value: "text",
			})
			break
		case discordgo.ChannelTypeGuildVoice:
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_type",
				Value: "voice",
			})
			break
		}

		options = append(options, models.ElasticEventlogOption{
			Key:   "channel_topic",
			Value: channel.Topic,
		})

		if channel.NSFW {
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_nsfw",
				Value: "yes",
			})
		} else {
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_nsfw",
				Value: "no",
			})
		}

		if channel.Bitrate > 0 {
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_bitrate",
				Value: strconv.Itoa(channel.Bitrate),
			})
		}

		if channel.Position > 0 {
			options = append(options, models.ElasticEventlogOption{
				Key:   "channel_position",
				Value: strconv.Itoa(channel.Position),
			})
		}

		options = append(options, models.ElasticEventlogOption{
			Key:   "channel_parentid",
			Value: channel.ParentID,
		})

		/*
			TODO: handle permission overwrites
			options = append(options, models.ElasticEventlogOption{
				Key:   "permission_overwrites",
				Value: channel.PermissionOverwrites,
			})
		*/

		helpers.EventlogLog(leftAt, channel.GuildID, channel.ID, models.EventlogTargetTypeChannel, "", models.EventlogTypeChannelDelete, "", nil, options)
	}()
}
